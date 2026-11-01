"""Stdlib-only client for the Runbox HTTP API."""

from __future__ import annotations

import http.client
import json
import os
import secrets
import shutil
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, Iterable, Iterator, List, Optional, Tuple, Union

from .errors import RunboxError
from .identity import Identity

PathLike = Union[str, "os.PathLike[str]"]
FileLike = Any

DEFAULT_TIMEOUT = 330.0
CHUNK = 1 << 20


@dataclass
class FileUpload:
    """One file sent in a multipart request.

    Use ``data`` for bytes already in memory, ``path`` to stream from disk, or
    ``stream`` for any file-like object: an open file, ``io.BytesIO``, a
    ``SpooledTemporaryFile`` behind an upload handler, or a pipe. Exactly one
    must be set.

    ``stream`` objects are read lazily and never closed by the client, so a
    handler can hand over its incoming upload directly instead of writing it to
    disk first. When the length is unknown the request is sent with
    ``Transfer-Encoding: chunked``.
    """

    name: str
    data: Optional[bytes] = None
    path: Optional[PathLike] = None
    stream: Optional[FileLike] = None
    size: Optional[int] = None
    content_type: str = "application/octet-stream"

    def __post_init__(self) -> None:
        given = [self.data is not None, self.path is not None, self.stream is not None]
        if sum(given) != 1:
            raise ValueError("set exactly one of data, path or stream")
        if self.path is not None:
            self.size = os.path.getsize(self.path)
        elif self.data is not None:
            self.size = len(self.data)
        elif self.size is None:
            self.size = _remaining_size(self.stream)

    def open(self):
        if self.path is not None:
            return open(self.path, "rb")
        if self.data is not None:
            import io

            return io.BytesIO(self.data)
        return self.stream


class Client:
    """A Runbox HTTP client."""

    def __init__(
        self,
        base_url: str,
        api_key: str = "",
        identity: Optional[Identity] = None,
        timeout: float = DEFAULT_TIMEOUT,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        if not self.base_url.startswith(("http://", "https://")):
            raise ValueError("base_url must be an absolute http(s) URL")
        self.api_key = api_key
        self.identity = identity
        self.timeout = timeout
        parts = urllib.parse.urlsplit(self.base_url)
        self._scheme = parts.scheme
        self._host = parts.hostname or ""
        self._port = parts.port
        self._prefix = parts.path.rstrip("/")

    def _resolve_identity(self, identity: Optional[Identity]) -> Optional[Identity]:
        return identity or self.identity

    def _headers(
        self,
        identity: Optional[Identity] = None,
        extra: Optional[Dict[str, str]] = None,
    ) -> Dict[str, str]:
        headers = {"Accept": "application/json", "User-Agent": "runbox-python/0.2.4"}
        selected = self._resolve_identity(identity)
        if selected is not None:
            headers.update(selected.headers())
        if self.api_key:
            headers["Authorization"] = "Bearer " + self.api_key
        if extra:
            headers.update(extra)
        return headers

    def _url(self, path: str, query: Optional[Dict[str, str]] = None) -> str:
        url = self.base_url + path
        if query:
            url += "?" + urllib.parse.urlencode(query, doseq=True)
        return url

    def _connection(self) -> http.client.HTTPConnection:
        if self._scheme == "https":
            return http.client.HTTPSConnection(self._host, self._port, timeout=self.timeout)
        return http.client.HTTPConnection(self._host, self._port, timeout=self.timeout)

    def _json(
        self,
        method: str,
        path: str,
        payload: Optional[Dict[str, Any]] = None,
        identity: Optional[Identity] = None,
        timeout: Optional[float] = None,
    ) -> Any:
        body = json.dumps(payload).encode() if payload is not None else None
        extra = {"Content-Type": "application/json"} if body is not None else None
        request = urllib.request.Request(
            self._url(path),
            data=body,
            method=method,
            headers=self._headers(identity, extra),
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout or self.timeout) as response:
                data = response.read()
        except urllib.error.HTTPError as error:
            raise _error(error.code, error.read()) from None
        if not data:
            return None
        return json.loads(data)

    def _text(
        self,
        method: str,
        path: str,
        identity: Optional[Identity] = None,
    ) -> str:
        request = urllib.request.Request(
            self._url(path),
            method=method,
            headers=self._headers(identity),
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                return response.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as error:
            raise _error(error.code, error.read()) from None

    def health(self) -> Dict[str, Any]:
        return self._json("GET", "/health")

    def ready(self) -> Dict[str, Any]:
        return self._json("GET", "/ready")

    def capabilities(self) -> Dict[str, Any]:
        return self._json("GET", "/v1/capabilities")

    def metrics(self) -> str:
        """Prometheus exposition text from GET /metrics."""
        return self._text("GET", "/metrics")

    def wait_ready(
        self,
        timeout: float = 30.0,
        interval: float = 0.5,
        identity: Optional[Identity] = None,
    ) -> Dict[str, Any]:
        """Poll GET /ready until it answers, or raise after ``timeout`` seconds."""
        deadline = time.monotonic() + timeout
        last: Optional[Exception] = None
        while True:
            try:
                return self._json("GET", "/ready", identity=identity, timeout=min(self.timeout, 10))
            except RunboxError as error:
                last = error
                if error.status != 503:
                    raise
            except OSError as error:  # connection refused while the runner boots
                last = error
            if time.monotonic() >= deadline:
                raise TimeoutError(f"runner did not become ready in {timeout}s") from last
            time.sleep(interval)

    def exec(
        self,
        command: str,
        timeout: int = 60,
        files: Optional[Iterable[FileUpload]] = None,
        identity: Optional[Identity] = None,
    ) -> Dict[str, Any]:
        file_list = list(files or [])
        request_timeout = max(self.timeout, timeout + 30)
        if not file_list:
            return self._json(
                "POST",
                "/v1/exec",
                {"command": command, "timeout_seconds": timeout},
                identity=identity,
                timeout=request_timeout,
            )
        fields: List[Tuple[str, str]] = [("command", command), ("timeout_seconds", str(timeout))]
        return self._multipart("/v1/exec", fields, file_list, identity, request_timeout)

    def upload(self, name: str, data: bytes) -> Dict[str, Any]:
        return self.upload_batch([FileUpload(name=name, data=data)])[0]

    def upload_stream(
        self,
        name: str,
        stream: FileLike,
        size: Optional[int] = None,
        identity: Optional[Identity] = None,
        content_type: str = "application/octet-stream",
    ) -> Dict[str, Any]:
        """Upload from an open file-like object without touching the disk.

        Pass the upload object an HTTP framework already gave you, for example
        ``request.files["f"].stream`` (Flask) or ``upload.file`` (FastAPI). The
        stream is not closed.
        """
        uploaded = self.upload_batch(
            [FileUpload(name=name, stream=stream, size=size, content_type=content_type)],
            identity=identity,
        )
        return uploaded[0]

    def upload_file(self, path: PathLike) -> Dict[str, Any]:
        return self.upload_batch([FileUpload(name=os.path.basename(os.fspath(path)), path=path)])[0]

    def upload_batch(
        self,
        files: Iterable[FileUpload],
        identity: Optional[Identity] = None,
    ) -> List[Dict[str, Any]]:
        uploaded = self._multipart("/v1/files/batch", [], list(files), identity, self.timeout)
        return list(uploaded.get("files", []))

    def list(self, path: str = "", identity: Optional[Identity] = None) -> List[Dict[str, Any]]:
        query = {"path": path} if path else None
        data = self._json("GET", self._url_path("/v1/list", query), identity=identity)
        return list(data.get("entries", []))

    def delete_workspace(self, identity: Optional[Identity] = None) -> None:
        self._json("DELETE", "/v1/workspace", identity=identity)

    def open_path(
        self,
        path: str,
        identity: Optional[Identity] = None,
        range_header: Optional[str] = None,
    ) -> http.client.HTTPResponse:
        """Open a workspace file as a readable stream.

        The caller owns the response and must close it. Useful to hand the body
        straight to a framework response, for example
        ``StreamingResponse(iter(response))``.
        """
        extra = {"Range": range_header} if range_header else None
        return self._open(self._url_path("/v1/path", {"path": path}), identity, extra)

    def open_archive(
        self,
        paths: Iterable[str],
        identity: Optional[Identity] = None,
    ) -> http.client.HTTPResponse:
        """Open a tar archive of several paths as a readable stream."""
        query = [("path", path) for path in paths]
        if not query:
            raise ValueError("at least one path is required")
        return self._open(self._url_path("/v1/archive", query), identity)

    def iter_path(
        self,
        path: str,
        chunk_size: int = 65536,
        identity: Optional[Identity] = None,
        range_header: Optional[str] = None,
    ) -> Iterator[bytes]:
        """Yield a workspace file in chunks without buffering it.

        Hand the iterator straight to a framework response, for example
        ``StreamingResponse(client.iter_path("out/report.csv"))``.
        """
        response = self.open_path(path, identity=identity, range_header=range_header)
        try:
            while True:
                chunk = response.read(chunk_size)
                if not chunk:
                    break
                yield chunk
        finally:
            response.close()

    def iter_archive(
        self,
        paths: Iterable[str],
        chunk_size: int = 65536,
        identity: Optional[Identity] = None,
    ) -> Iterator[bytes]:
        """Yield a tar archive in chunks without buffering it."""
        response = self.open_archive(paths, identity=identity)
        try:
            while True:
                chunk = response.read(chunk_size)
                if not chunk:
                    break
                yield chunk
        finally:
            response.close()

    def download_path(
        self,
        path: str,
        destination: Union[PathLike, FileLike],
        identity: Optional[Identity] = None,
        range_header: Optional[str] = None,
    ) -> None:
        extra = {"Range": range_header} if range_header else None
        self._download(self._url_path("/v1/path", {"path": path}), destination, identity, extra)

    def download_path_to(
        self,
        path: str,
        destination: FileLike,
        identity: Optional[Identity] = None,
        range_header: Optional[str] = None,
    ) -> None:
        """Download into an open file-like object instead of a path."""
        self.download_path(path, destination, identity, range_header)

    def download_archive(
        self,
        paths: Iterable[str],
        destination: Union[PathLike, FileLike],
        identity: Optional[Identity] = None,
    ) -> None:
        query = [("path", path) for path in paths]
        if not query:
            raise ValueError("at least one path is required")
        self._download(self._url_path("/v1/archive", query), destination, identity)

    def download_archive_to(
        self,
        paths: Iterable[str],
        destination: FileLike,
        identity: Optional[Identity] = None,
    ) -> None:
        """Download a tar archive into an open file-like object."""
        self.download_archive(paths, destination, identity)

    def _open(
        self,
        path: str,
        identity: Optional[Identity],
        extra: Optional[Dict[str, str]] = None,
    ) -> http.client.HTTPResponse:
        request = urllib.request.Request(
            self.base_url + path,
            headers=self._headers(identity, extra),
            method="GET",
        )
        try:
            return urllib.request.urlopen(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            raise _error(error.code, error.read()) from None

    def _download(
        self,
        path: str,
        destination: Union[PathLike, FileLike],
        identity: Optional[Identity],
        extra: Optional[Dict[str, str]] = None,
    ) -> None:
        response = self._open(path, identity, extra)
        with response:
            if hasattr(destination, "write"):
                shutil.copyfileobj(response, destination, length=CHUNK)
                return
            with open(os.fspath(destination), "wb") as output:
                shutil.copyfileobj(response, output, length=CHUNK)

    @staticmethod
    def _url_path(path: str, query: Any = None) -> str:
        if not query:
            return path
        return path + "?" + urllib.parse.urlencode(query, doseq=True)

    def _multipart(
        self,
        path: str,
        fields: List[Tuple[str, str]],
        files: List[FileUpload],
        identity: Optional[Identity],
        timeout: float,
    ) -> Any:
        boundary = secrets.token_hex(16)
        headers = self._headers(
            identity,
            {"Content-Type": f"multipart/form-data; boundary={boundary}"},
        )
        chunks = list(self._iter_parts(boundary, fields, files))
        known = all(file.size is not None for file in files)

        connection = self._connection()
        try:
            if known:
                length = sum(_chunk_size(chunk) for chunk in chunks)
                connection.putrequest("POST", self._prefix + path)
                for key, value in headers.items():
                    connection.putheader(key, value)
                connection.putheader("Content-Length", str(length))
                connection.endheaders()
                for chunk in chunks:
                    if isinstance(chunk, (bytes, bytearray)):
                        connection.send(chunk)
                    else:
                        _send_stream(connection, chunk)
            else:
                # Unknown length: http.client switches to chunked framing when
                # the body is an iterable and no Content-Length is given.
                connection.request(
                    "POST",
                    self._prefix + path,
                    body=self._flatten(chunks),
                    headers=headers,
                )
            response = connection.getresponse()
            body = response.read()
        finally:
            connection.close()

        if response.status < 200 or response.status >= 300:
            raise _error(response.status, body)
        return json.loads(body) if body else None

    def _iter_parts(
        self,
        boundary: str,
        fields: List[Tuple[str, str]],
        files: List[FileUpload],
    ):
        """Yield bytes and file objects in multipart order."""
        for name, value in fields:
            yield (
                f"--{boundary}\r\n"
                f'Content-Disposition: form-data; name="{_escape(name)}"\r\n\r\n'
            ).encode()
            yield value.encode()
            yield b"\r\n"

        for file in files:
            yield (
                f"--{boundary}\r\n"
                f'Content-Disposition: form-data; name="files"; filename="{_escape(file.name)}"\r\n'
                f"Content-Type: {file.content_type}\r\n\r\n"
            ).encode()
            yield _PartBody(file)
            yield b"\r\n"

        yield f"--{boundary}--\r\n".encode()

    @staticmethod
    def _flatten(chunks):
        for chunk in chunks:
            if isinstance(chunk, (bytes, bytearray)):
                yield bytes(chunk)
            else:
                yield from chunk


class _PartBody:
    """A lazily read file body with a known number of bytes, when available."""

    def __init__(self, file: FileUpload) -> None:
        self.file = file
        self.size = file.size

    def __iter__(self):
        file = self.file
        if file.data is not None:
            yield file.data
            return
        if file.path is not None:
            with open(file.path, "rb") as handle:
                while True:
                    chunk = handle.read(CHUNK)
                    if not chunk:
                        break
                    yield chunk
            return
        while True:
            chunk = file.stream.read(CHUNK)
            if not chunk:
                break
            yield chunk


def _chunk_size(chunk: Any) -> int:
    if isinstance(chunk, (bytes, bytearray)):
        return len(chunk)
    return int(chunk.size or 0)


def _send_stream(connection: http.client.HTTPConnection, body: _PartBody) -> None:
    for chunk in body:
        connection.send(chunk)


def _remaining_size(stream: FileLike) -> Optional[int]:
    """Best-effort length of the unread part of a seekable stream."""
    try:
        if not stream.seekable():
            return None
        position = stream.tell()
        end = stream.seek(0, os.SEEK_END)
        stream.seek(position, os.SEEK_SET)
        return max(0, end - position)
    except (AttributeError, OSError, ValueError):
        return None


def _escape(value: str) -> str:
    return value.replace("\\", "_").replace('"', "_").replace("\r", "_").replace("\n", "_")


def _error(status: int, body: bytes) -> RunboxError:
    code = f"http_{status}"
    message = body.decode("utf-8", "replace").strip()
    try:
        payload = json.loads(body)
        code = str(payload.get("code") or code)
        message = str(payload.get("message") or message)
    except (ValueError, TypeError):
        pass
    if not message or message.startswith("<"):
        message = f"HTTP {status}"
    return RunboxError(status, code, message[:2048])
