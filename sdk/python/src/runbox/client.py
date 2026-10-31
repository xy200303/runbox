"""Stdlib-only client for the Runbox HTTP API."""

from __future__ import annotations

import http.client
import json
import os
import secrets
import shutil
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, Iterable, List, Optional, Tuple, Union

from .errors import RunboxError
from .identity import Identity

PathLike = Union[str, "os.PathLike[str]"]

DEFAULT_TIMEOUT = 330.0


@dataclass
class FileUpload:
    """One file sent in a multipart request.

    Use ``data`` for bytes already in memory, or ``path`` to stream from disk.
    Exactly one must be set.
    """

    name: str
    data: Optional[bytes] = None
    path: Optional[PathLike] = None
    size: Optional[int] = None

    def __post_init__(self) -> None:
        if (self.data is None) == (self.path is None):
            raise ValueError("set exactly one of data or path")
        if self.path is not None:
            self.size = os.path.getsize(self.path)
        elif self.size is None:
            self.size = len(self.data or b"")

    def open(self):
        if self.path is not None:
            return open(self.path, "rb")
        import io

        return io.BytesIO(self.data or b"")


@dataclass(frozen=True)
class _Part:
    header: bytes
    length: int
    value: bytes
    path: Optional[PathLike] = None


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
        headers = {"Accept": "application/json", "User-Agent": "runbox-python/0.2.0"}
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

    def health(self) -> Dict[str, Any]:
        return self._json("GET", "/health")

    def capabilities(self) -> Dict[str, Any]:
        return self._json("GET", "/v1/capabilities")

    def exec(
        self,
        command: str,
        timeout: int = 60,
        inputs: Optional[Iterable[str]] = None,
        files: Optional[Iterable[FileUpload]] = None,
        identity: Optional[Identity] = None,
    ) -> Dict[str, Any]:
        input_list = list(inputs or [])
        file_list = list(files or [])
        request_timeout = max(self.timeout, timeout + 30)
        if not file_list:
            return self._json(
                "POST",
                "/v1/exec",
                {
                    "command": command,
                    "timeout_seconds": timeout,
                    "inputs": input_list,
                },
                identity=identity,
                timeout=request_timeout,
            )
        fields: List[Tuple[str, str]] = [("command", command), ("timeout_seconds", str(timeout))]
        fields.extend(("inputs", value) for value in input_list)
        return self._multipart("/v1/exec", fields, file_list, identity, request_timeout)

    def upload(self, name: str, data: bytes) -> Dict[str, Any]:
        return self.upload_batch([FileUpload(name=name, data=data)])[0]

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

    def download_file(
        self,
        file_id: str,
        destination: PathLike,
        identity: Optional[Identity] = None,
        range_header: Optional[str] = None,
    ) -> None:
        extra = {"Range": range_header} if range_header else None
        self._download(
            "/v1/files/" + urllib.parse.quote(file_id, safe=""),
            destination,
            identity,
            extra,
        )

    def download_path(
        self,
        path: str,
        destination: PathLike,
        identity: Optional[Identity] = None,
        range_header: Optional[str] = None,
    ) -> None:
        extra = {"Range": range_header} if range_header else None
        self._download(self._url_path("/v1/path", {"path": path}), destination, identity, extra)

    def download_archive(
        self,
        paths: Iterable[str],
        destination: PathLike,
        identity: Optional[Identity] = None,
    ) -> None:
        query = [("path", path) for path in paths]
        if not query:
            raise ValueError("at least one path is required")
        self._download(self._url_path("/v1/archive", query), destination, identity)

    def _download(
        self,
        path: str,
        destination: PathLike,
        identity: Optional[Identity],
        extra: Optional[Dict[str, str]] = None,
    ) -> None:
        request = urllib.request.Request(
            self.base_url + path,
            headers=self._headers(identity, extra),
            method="GET",
        )
        try:
            response = urllib.request.urlopen(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            raise _error(error.code, error.read()) from None
        with response, open(destination, "wb") as output:
            shutil.copyfileobj(response, output, length=1 << 20)

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
        parts: List[_Part] = []

        for name, value in fields:
            body = value.encode()
            header = (
                f"--{boundary}\r\n"
                f'Content-Disposition: form-data; name="{_escape(name)}"\r\n\r\n'
            ).encode()
            parts.append(_Part(header=header, length=len(body), value=body))

        for file in files:
            safe_name = _escape(file.name)
            header = (
                f"--{boundary}\r\n"
                f'Content-Disposition: form-data; name="files"; filename="{safe_name}"\r\n'
                "Content-Type: application/octet-stream\r\n\r\n"
            ).encode()
            parts.append(
                _Part(
                    header=header,
                    length=int(file.size or 0),
                    value=b"",
                    path=file.path,
                )
                if file.path is not None
                else _Part(header=header, length=len(file.data or b""), value=file.data or b"")
            )

        closing = f"\r\n--{boundary}--\r\n".encode()
        total = sum(part.length + len(part.header) for part in parts) + len(closing)
        total += sum(2 for _ in parts)  # CRLF after each part body

        connection = self._connection()
        try:
            connection.putrequest("POST", self._prefix + path)
            headers = self._headers(
                identity,
                {"Content-Type": f"multipart/form-data; boundary={boundary}"},
            )
            for key, value in headers.items():
                connection.putheader(key, value)
            connection.putheader("Content-Length", str(total))
            connection.endheaders()
            for part in parts:
                connection.send(part.header)
                if part.path is not None:
                    with open(part.path, "rb") as handle:
                        while True:
                            chunk = handle.read(1 << 20)
                            if not chunk:
                                break
                            connection.send(chunk)
                else:
                    connection.send(part.value)
                connection.send(b"\r\n")
            connection.send(closing)
            response = connection.getresponse()
            body = response.read()
        finally:
            connection.close()

        if response.status < 200 or response.status >= 300:
            raise _error(response.status, body)
        return json.loads(body) if body else None


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
