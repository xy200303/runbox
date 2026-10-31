# Runbox Python SDK

Python client for [Runbox](https://runbox.xyun.dev), the lightweight sandbox for
agent commands and files. Standard library only — no `requests`, no build step.

```bash
pip install runbox-sdk
```

## Quick start

```python
import os
from runbox import Client, Identity

client = Client(
    "http://localhost:8099",
    api_key=os.environ["RUNBOX_API_KEY"],
    identity=Identity(namespace="team-a", principal="user-42"),
)

result = client.exec('python3 -c "print(6 * 7)"', timeout=30)
print(result["exit_code"], result["stdout"])      # 0 42
```

The client is safe to reuse across threads: bind an identity once, or pass
`identity=` on any call.

## Identity

Exactly one identity form per request:

| Form | Constructor | Headers sent |
| --- | --- | --- |
| Opaque token | `Identity(token="user-42")` | `X-Runbox-Identity` |
| Tenant + user | `Identity(namespace="team-a", principal="user-42")` | `X-Runbox-Namespace` + `X-Runbox-Principal` |

Supplying both forms, or half of the scoped form, returns `400` from the
runner.

## Run a command

```python
result = client.exec(
    "python3 analyze.py files/input.csv",
    timeout=60,
    files=[FileUpload(name="input.csv", path="input.csv")],   # streamed from disk
)

with open("input.csv", "rb") as handle:
    result = client.exec(
        "cat files/input.csv",
        files=[FileUpload(name="input.csv", stream=handle)],  # streamed, never closed
    )
```

Attachments land in `files/<name>` inside the workspace; the command refers to
them by that path. `exec` returns the parsed JSON response, including
`exit_code`, `stdout`, `stderr`, `files` and `duration_ms`.

## Files

```python
uploaded = client.upload_file("report.xlsx")       # -> {"path": "files/report.xlsx", ...}
client.upload("notes.txt", b"hello")
client.upload_batch([FileUpload(name="a.csv", path="a.csv"), FileUpload(name="b.csv", path="b.csv")])
entries = client.list("out")
client.delete_workspace()                          # keeps the identity and its UID
```

Downloads can go to a path or to any file-like object:

```python
client.download_path("out/report.csv", "report.csv")
client.download_path_to("out/report.csv", sink)    # sink: anything with .write()
client.download_archive(["out", "tmp"], "artifacts.tar")
client.download_archive_to(["out"], sink)
```

## Streaming and forwarding

`open_path` returns the raw `http.client.HTTPResponse`, so nothing is buffered.
`iter_path` yields chunks, which is what a framework response wants:

```python
from fastapi import FastAPI
from fastapi.responses import StreamingResponse

app = FastAPI()

@app.get("/files")
def download(path: str):
    return StreamingResponse(client.iter_path(path), media_type="application/octet-stream")
```

Forwarding a byte range (video scrubbing, resumable downloads) works the same
way; the runner answers with `206` and `Content-Range`, and the SDK passes the
body straight through:

```python
@app.get("/files")
def download(path: str, range: str | None = None):
    return StreamingResponse(client.iter_path(path, range_header=range))
```

A whole directory as one tar stream:

```python
@app.get("/archive")
def archive():
    return StreamingResponse(
        client.iter_archive(["out", "tmp/step2"]),
        media_type="application/x-tar",
        headers={"Content-Disposition": 'attachment; filename="artifacts.tar"'},
    )
```

If you need the response itself — headers, status, manual chunking — use
`open_path` / `open_archive` and close what you get back:

```python
response = client.open_path("out/report.pdf", range_header="bytes=0-1023")
try:
    print(response.status, response.getheader("Content-Length"))
    for chunk in iter(lambda: response.read(65536), b""):
        handle(chunk)
finally:
    response.close()
```

Streaming stops when the client disconnects: the generator's `finally` closes
the response, and the runner notices the closed connection.

## Health and limits

```python
client.health()
client.ready()
client.capabilities()                  # limits and detected tooling
client.wait_ready(timeout=30)
client.metrics()
```

## Errors

Every non-2xx response raises `RunboxError` with `status`, `code` and
`message`:

```python
from runbox import RunboxError

try:
    client.exec("rm -rf out && echo gone")
except RunboxError as error:
    if error.code in ("busy", "quota_exceeded"):
        ...                            # retry later, or clean the workspace
    elif error.code == "timeout":
        ...                            # raise the timeout
    else:
        raise
```

Common codes: `400` bad identity or path, `401` bad bearer token, `404` path
missing or owned by another identity, `409` `busy` / `quota_exceeded`, `413`
request too large.

## Links

- API reference: [docs/api.md](../../docs/api.md)
- Deployment and hardening: [docs/deployment.md](../../docs/deployment.md)
- Changelog: [CHANGELOG.md](../../CHANGELOG.md)
