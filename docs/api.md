# HTTP API

Base URL: `http://host:8099` by default.

## Authentication and identity

Every endpoint except `GET /health` and `GET /ready` requires the runner API key:

```
Authorization: Bearer <RUNBOX_API_KEY>
```

The comparison is constant time; there is no per-key scope, so treat the token
as a full-admin secret.

Every data endpoint requires an identity in **exactly one** of two forms.

Single token:

```http
X-Runbox-Identity: 01J...
```

Namespace + principal:

```http
X-Runbox-Namespace: tenant-a
X-Runbox-Principal: user-42
```

- Token: 1-256 visible ASCII characters without spaces.
- Namespace and principal: each `[A-Za-z0-9._-]{1,128}`.
- Supplying both forms, or only half of namespace+principal, returns
  `400 bad_request`; Runbox never merges two identity sources.
- The server hashes the identity into a canonical storage key (SHA-256). The
  key, not the raw value, is used for workspaces, UIDs and file IDs.
- For scoped identities, namespace/principal are stored in the `identities`
  table of `state/runbox.db` for tenant-level operations. Tokens are not
  persisted.

Responses carry `X-Request-Id`. Include it when reporting a problem; the same ID
appears in the runner log.

## File IDs

```text
rbx_ + base64url(version, identity key, kind, path)
```

`version` is `1`, `identity key` is the canonical hash, and `kind` is `files`,
`out` or `keep`. A file ID is accepted only with the identity that owns it.
There is no legacy format and no cross-identity file access.

## Errors

All errors are JSON:

```json
{"code": "busy", "message": "runner is at capacity, try again shortly"}
```

| Status | Code | Meaning |
|---|---|---|
| 400 | `bad_request` | Missing/invalid field, path, identity or file id. |
| 401 | `unauthorized` | Missing or invalid `Authorization: Bearer` token. |
| 403 | `forbidden` | The file belongs to another workspace. |
| 404 | `not_found` | File, directory or workspace entry does not exist. |
| 408 | `canceled` | The client canceled the request. |
| 409 | `busy` | The runner is at its concurrency limit. |
| 409 | `timeout` | The command exceeded its timeout and was killed. |
| 409 | `quota_exceeded` | Workspace or archive quota exceeded. |
| 413 | `too_large` | Upload or request body exceeds the limit. |
| 415 | `unsupported_media_type` | Unsupported request content type. |
| 500 | `internal` | Unexpected failure. |

## GET /health

No authentication. Suitable for container probes.

```json
{
  "status": "healthy",
  "version": "0.1.0",
  "uptime_seconds": 42
}
```

## GET /ready

No authentication. Checks that the data directories exist and that the state
directory is writable.

```json
{"status": "ready"}
```

A failed check returns `503` with `{"status": "not_ready", "message": "..."}`.
`/ready` is the endpoint to use for orchestrator readiness; `/health` is
liveness only.

## GET /metrics

Requires `Authorization: Bearer <RUNBOX_API_KEY>`. Returns
Prometheus text format (`text/plain; version=0.0.4`).

Labels are deliberately bounded to status classes and error reasons; identities
and paths never become labels.

| Metric | Type | Meaning |
|---|---|---|
| `runbox_requests_total` (label `status`) | counter | HTTP requests by status class |
| `runbox_exec_total` | counter | Commands accepted for execution |
| `runbox_exec_errors_total` (label `reason`) | counter | Failed executions by reason |
| `runbox_upload_bytes_total` | counter | Bytes actually read from uploads |
| `runbox_download_bytes_total` | counter | Bytes streamed to clients |
| `runbox_archive_total` | counter | Files archived into `keep/` |
| `runbox_workspace_deletes_total` | counter | Workspaces deleted |
| `runbox_active_execs` | gauge | Commands currently running |
| `runbox_uptime_seconds` | gauge | Process uptime |
| `runbox_build_info{version="..."}` | gauge | Build information |

## GET /v1/capabilities

```json
{
  "version": "0.1.0",
  "os": "linux",
  "arch": "amd64",
  "limits": {
    "max_upload_bytes": 20971520,
    "max_command_bytes": 65536,
    "max_inputs": 64,
    "max_stdout_bytes": 204800,
    "max_output_files": 20,
    "max_output_bytes": 209715200,
    "quota_bytes": 1073741824,
    "keep_bytes": 1073741824,
    "retention_seconds": 604800,
    "default_timeout_seconds": 60,
    "max_timeout_seconds": 300
  },
  "features": {
    "python": true,
    "node": true,
    "npm": true,
    "libreoffice": true,
    "pdftotext": true,
    "git": true
  }
}
```

`features` reports what was found at startup with `exec.LookPath`; a missing
entry means `false`.

## POST /v1/exec

Run one command in the caller's workspace. Two request encodings are accepted.

### JSON

```http
POST /v1/exec
Content-Type: application/json

{
  "command": "python3 analyze.py files/input.csv",
  "timeout_seconds": 60,
  "inputs": ["rbx_..."]
}
```

- `command` (required) is passed to `/bin/sh -c` on Linux and is limited to
  `RUNBOX_MAX_COMMAND_BYTES`.
- `timeout_seconds` is optional; `0`/absent uses `RUNBOX_DEFAULT_TIMEOUT` and
  values above `RUNBOX_MAX_TIMEOUT` are clamped.
- `inputs` are file IDs that must belong to the same identity. They are
  validated for ownership; the files are already on disk. At most
  `RUNBOX_MAX_INPUTS` inputs (including multipart `files`) are accepted.

### Multipart

```http
POST /v1/exec
Content-Type: multipart/form-data; boundary=...

command=python3 analyze.py files/input.csv
timeout_seconds=60
inputs=rbx_...
files=@input.csv
```

`files` parts are stored under `files/` before the command runs. The stored
name is flattened to one path segment and returned as an input file ID.

### Response

```json
{
  "exit_code": 0,
  "stdout": "hello\n",
  "files": [
    {"name": "report.csv", "file_id": "rbx_...", "size": 1280}
  ],
  "duration_ms": 321
}
```

- HTTP 200 means the command ran, **not** that it succeeded. Inspect `exit_code`.
- `stdout` contains stdout, then stderr when stderr is non-empty. Long output is
  truncated in the middle with a marker; the head and tail are preserved.
- `files` are regular files under `out/` touched during this call, with
  workspace-relative names (`report.csv`, `sub/chart.png`). Symlinks are
  skipped. The list is bounded by `RUNBOX_MAX_OUTPUT_FILES` and
  `RUNBOX_MAX_OUTPUT_BYTES`.
- An empty result is `[]`, not `null`.

### Failure semantics

| Situation | Result |
|---|---|
| Non-zero exit | 200 with `exit_code != 0` |
| Timeout | 409 `timeout` after killing the process group |
| Concurrency full | 409 `busy` (no queue) |
| Workspace over quota | 409 `quota_exceeded` |
| Client disconnect | 408 `canceled`; the process group is killed |

## POST /v1/files

Upload one file into `files/`.

```bash
curl -fsS -H "Authorization: Bearer $KEY" \
  -H "X-Runbox-Identity: team-a.user-42" \
  -F "file=@input.csv" http://localhost:8099/v1/files
```

```json
{"file_id": "rbx_...", "name": "input.csv", "size": 1280}
```

The field may be named `file` or `files`. An existing file with the same name
is replaced. The upload is written through a size-limited reader, so a lying
`Content-Length` cannot fill the disk.

## POST /v1/files/batch

Upload several files in one multipart request. Every file part must use the
field name `files`:

```bash
curl -fsS -H "Authorization: Bearer $KEY" \
  -H "X-Runbox-Identity: team-a.user-42" \
  -F "files=@a.csv" -F "files=@b.csv" \
  http://localhost:8099/v1/files/batch
```

```json
{"files": [{"file_id": "rbx_...", "name": "a.csv", "size": 10},
           {"file_id": "rbx_...", "name": "b.csv", "size": 20}]}
```

The request is bounded by `RUNBOX_MAX_BATCH_FILES` and
`RUNBOX_MAX_REQUEST_BYTES`. Files stream to disk; none are buffered in memory.

## GET /v1/files/{id}

Download an uploaded (`files/`), produced (`out/`) or archived (`keep/`) file.
The response is `application/octet-stream` with:

```
Content-Disposition: attachment; filename="fallback.txt"; filename*=UTF-8''%E6%8A%A5%E8%A1%A8.xlsx
```

Clients should prefer `filename*` (RFC 5987) and fall back to `filename`.
Range and HEAD requests are supported. Send `Range: bytes=0-1023` to receive
`206 Partial Content`, or use `HEAD` to fetch headers without the body.

## POST /v1/files/{id}/keep

Copy a `files/` or `out/` entry into the immutable archive and return its new
file ID:

```json
{"name": "report.csv", "file_id": "rbx_...", "size": 1280}
```

- The copy lives under `<root>/keep/<key>/`, outside the
  writable workspace and owned by the runner, so later commands cannot modify
  it.
- Re-archiving an already archived ID is idempotent and returns the same path.
- The archive is subject to `RUNBOX_KEEP_BYTES`; exceeding it returns
  `409 quota_exceeded`.
- Archiving does not delete the `out/` original.

## GET /v1/path?path=out/report.csv

Download a file by workspace-relative path, which is what an agent sees when
it runs `ls`:

- `out/report.csv`, `files/photo.jpg`, `./out/x`, and `out\\x` (on Windows
  development) are normalized;
- absolute paths, `..`, drive letters and control characters are rejected;
- the resolved path must remain inside the workspace after following
  symlinks;
- directories are rejected.

## GET /v1/list?path=out

List one directory level. `path` is optional; an empty value lists the
workspace root.

```json
{
  "entries": [
    {
      "name": "report.csv",
      "path": "out/report.csv",
      "size": 1280,
      "is_dir": false,
      "modified": "2026-10-31T02:11:07Z"
    }
  ]
}
```

## GET /v1/archive?path=out&path=tmp/step.txt

Download files or directories as one tar stream, relative to the caller's
workspace. A directory is walked recursively; symlinks are skipped. The
archive is bounded by `RUNBOX_MAX_BATCH_FILES` and `RUNBOX_MAX_BATCH_BYTES`.

```bash
curl -fsS -H "Authorization: Bearer $KEY" \
  -H "X-Runbox-Identity: team-a.user-42" \
  "http://localhost:8099/v1/archive?path=out" -o artifacts.tar
```

The response is `application/x-tar` with
`Content-Disposition: attachment; filename="runbox-files.tar"`. This is the
batch download path for intermediate files; no file IDs are required.

## DELETE /v1/workspace[?purge=true]

Remove the caller's mutable workspace. Archived deliverables under `keep/` are
retained by default; `purge=true` removes them as well. The workspace is
recreated on the next call.

```json
{"deleted": true}
```

## Limits and concurrency

- One command per identity at a time; the runner serializes them.
- At most `RUNBOX_MAX_CONCURRENCY` commands in total. There is no queue:
  excess requests fail fast with `busy`.
- `RUNBOX_MAX_REQUEST_BYTES` covers the whole body, uploads included.
- The workspace quota is checked before each command and includes installs,
  caches and temporary files under the workspace.

## Versioning

This page describes the only contract. Breaking changes are recorded in the
changelog and require the client and server to move together; the HTTP API does
not negotiate versions.
