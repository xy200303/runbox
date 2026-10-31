#!/usr/bin/env python3
"""Minimal stdlib-only Runbox client and CLI.

Environment:
    RUNBOX_URL        default http://localhost:8099
    RUNBOX_API_KEY    required unless the runner runs in anonymous mode
    RUNBOX_IDENTITY   single token, optional
    RUNBOX_NAMESPACE  scoped mode, requires RUNBOX_PRINCIPAL
    RUNBOX_PRINCIPAL  scoped mode, requires RUNBOX_NAMESPACE

Examples:
    python examples/python/runbox_client.py exec "echo hello"
    python examples/python/runbox_client.py upload ./data.csv
    python examples/python/runbox_client.py list out
    python examples/python/runbox_client.py download out/hello.txt ./out.bin
"""

from __future__ import annotations

import email.message
import json
import os
import secrets
import sys
import urllib.error
import urllib.parse
import urllib.request


class RunboxError(Exception):
    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(f"{status} {code}: {message}")
        self.status = status
        self.code = code
        self.message = message


class Runbox:
    def __init__(
        self,
        base_url: str,
        api_key: str,
        identity: str = "",
        namespace: str = "",
        principal: str = "",
    ) -> None:
        if identity and (namespace or principal):
            raise ValueError("use either identity or namespace+principal, not both")
        if not identity and not (namespace and principal):
            raise ValueError("identity or namespace+principal is required")
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.identity = identity
        self.namespace = namespace
        self.principal = principal

    def _headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        headers = {"Accept": "application/json"}
        if self.identity:
            headers["X-Runbox-Identity"] = self.identity
        else:
            headers["X-Runbox-Namespace"] = self.namespace
            headers["X-Runbox-Principal"] = self.principal
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        if extra:
            headers.update(extra)
        return headers

    def _request(
        self,
        method: str,
        path: str,
        body: bytes | None = None,
        headers: dict[str, str] | None = None,
        timeout: float = 330,
    ) -> urllib.response.addinfourl:
        request = urllib.request.Request(
            self.base_url + path,
            data=body,
            method=method,
            headers=self._headers(headers),
        )
        try:
            return urllib.request.urlopen(request, timeout=timeout)
        except urllib.error.HTTPError as error:
            payload = error.read().decode("utf-8", "replace")
            try:
                parsed = json.loads(payload)
            except ValueError:
                parsed = {}
            raise RunboxError(
                error.code,
                parsed.get("code", f"http_{error.code}"),
                parsed.get("message", payload),
            ) from None

    def health(self) -> dict:
        with self._request("GET", "/health") as response:
            return json.load(response)

    def capabilities(self) -> dict:
        with self._request("GET", "/v1/capabilities") as response:
            return json.load(response)

    def exec(self, command: str, timeout: int = 60, inputs: list[str] | None = None) -> dict:
        body = json.dumps(
            {"command": command, "timeout_seconds": timeout, "inputs": inputs or []}
        ).encode()
        with self._request(
            "POST",
            "/v1/exec",
            body,
            {"Content-Type": "application/json"},
            timeout=timeout + 30,
        ) as response:
            return json.load(response)

    def upload(self, path: str) -> dict:
        name = os.path.basename(path)
        with open(path, "rb") as handle:
            content = handle.read()
        boundary = secrets.token_hex(16)
        safe_name = name.replace('"', "_")
        head = (
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="file"; filename="{safe_name}"\r\n'
            "Content-Type: application/octet-stream\r\n\r\n"
        ).encode()
        tail = f"\r\n--{boundary}--\r\n".encode()
        with self._request(
            "POST",
            "/v1/files",
            head + content + tail,
            {"Content-Type": f"multipart/form-data; boundary={boundary}"},
        ) as response:
            return json.load(response)

    def open_path(self, path: str, destination: str) -> str:
        quoted = urllib.parse.quote(path, safe="")
        with self._request("GET", f"/v1/path?path={quoted}") as response:
            with open(destination, "wb") as handle:
                while chunk := response.read(1 << 20):
                    handle.write(chunk)
            return _filename(response.headers.get("Content-Disposition", "")) or path

    def list(self, path: str = "") -> list[dict]:
        suffix = f"?path={urllib.parse.quote(path)}" if path else ""
        with self._request("GET", "/v1/list" + suffix) as response:
            return json.load(response)["entries"]


def _filename(header: str) -> str:
    if not header:
        return ""
    message = email.message.Message()
    message["Content-Disposition"] = header
    return message.get_filename() or ""


def main() -> int:
    identity = os.environ.get("RUNBOX_IDENTITY", "")
    namespace = os.environ.get("RUNBOX_NAMESPACE", "")
    principal = os.environ.get("RUNBOX_PRINCIPAL", "")
    if not identity and not (namespace and principal):
        identity = "demo.alice"
    client = Runbox(
        os.environ.get("RUNBOX_URL", "http://localhost:8099"),
        os.environ.get("RUNBOX_API_KEY", ""),
        identity=identity,
        namespace=namespace,
        principal=principal,
    )
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 2
    command, rest = args[0], args[1:]
    if command == "health":
        print(json.dumps(client.health(), indent=2))
    elif command == "caps":
        print(json.dumps(client.capabilities(), indent=2))
    elif command == "exec":
        print(json.dumps(client.exec(" ".join(rest)), indent=2))
    elif command == "upload":
        print(json.dumps(client.upload(rest[0]), indent=2))
    elif command == "download":
        print(client.open_path(rest[0], rest[1]))
    elif command == "list":
        print(json.dumps(client.list(rest[0] if rest else ""), indent=2))
    else:
        print(f"unknown command: {command}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RunboxError as error:
        print(error, file=sys.stderr)
        raise SystemExit(1)
