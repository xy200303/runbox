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
  key, not the raw value, is used for workspaces, UIDs and locks.
- For scoped identities, namespace/principal are stored in the `identities`
  table of `state/runbox.db` for tenant-level operations. Tokens are not
  persisted.

Responses carry `X-Request-Id`. Include it when reporting a problem; the same ID
appears in the runner log.

## Errors

All errors are JSON:

```json
{"code": "busy", "message": "runner is at capacity, try again shortly"}
```

| Status | Code | Meaning |
|---|---|---|
| 400 | `bad_request` | Missing/invalid field, path or identity. |
| 401 | `unauthorized` | Missing or invalid `Authorization: Bearer` token. |
| 403 | `forbidden` | The file belongs to another workspace. |
| 404 | `not_found` | File, directory or workspace entry does not exist. |
| 408 | `canceled` | The client canceled the request. |
| 409 | `busy` | The runner is at its concurrency limit. |
| 409 | `timeout` | The command exceeded its timeout and was killed. |
| 409 | `quota_exceeded` | Workspace quota exceeded; free space with `DELETE /v1/path?path=...` or `DELETE /v1/workspace`. |
| 409 | `uid_exhausted` | No free UID in the configured range; the pool spans `RUNBOX_UID_MIN`..`RUNBOX_UID_MAX` and a UID returns when its workspace is deleted or expires. |
| 413 | `too_large` | Upload or request body exceeds the limit. |
| 415 | `unsupported_media_type` | Unsupported request content type. |
| 500 | `internal` | Unexpected failure. |

## GET /health

No authentication. Suitable for container probes.

```json
{
  "status": "healthy",
  "version": "0.2.3",
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
| `runbox_workspace_deletes_total` | counter | Workspaces deleted |
| `runbox_active_execs` | gauge | Commands currently running |
| `runbox_uptime_seconds` | gauge | Process uptime |
| `runbox_build_info{version="..."}` | gauge | Build information |

## GET /v1/capabilities

```json
{
  "version": "0.2.3",
  "os": "linux",
  "arch": "amd64",
  "limits": {
    "max_upload_bytes": 20971520,
    "max_command_bytes": 65536,
    "max_attachments": 64,
    "max_stdout_bytes": 204800,
    "max_output_files": 20,
    "max_output_bytes": 209715200,
    "max_batch_files": 1000,
    "max_batch_bytes": 1073741824,
    "quota_bytes": 1073741824,
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
  "timeout_seconds": 60
}
```

- `command` (required) is passed to `/bin/sh -c` on Linux and is limited to
  `RUNBOX_MAX_COMMAND_BYTES`.
- `timeout_seconds` is optional; `0`/absent uses `RUNBOX_DEFAULT_TIMEOUT` and
  values above `RUNBOX_MAX_TIMEOUT` are clamped.
- Files are referenced by workspace-relative path, for example `files/input.csv`.

### Multipart

```http
POST /v1/exec
Content-Type: multipart/form-data; boundary=...

command=python3 analyze.py files/input.csv
timeout_seconds=60
files=@input.csv
```

`files` parts are stored under `files/` before the command runs. The stored
name is flattened to one path segment; refer to it as `files/<name>`. At most
`RUNBOX_MAX_ATTACHMENTS` attachments are accepted.

### Response

```json
{
  "exit_code": 0,
  "stdout": "hello\n",
  "files": [
    {"name": "report.csv", "path": "out/report.csv", "size": 1280}
  ],
  "duration_ms": 321
}
```

- HTTP 200 means the command ran, **not** that it succeeded. Inspect `exit_code`.
- `stdout` contains stdout, then stderr when stderr is non-empty. Long output is
  truncated in the middle with a marker; the head and tail are preserved.
- `files` are regular files under `out/` touched during this call, with
  workspace-relative paths (`out/report.csv`, `out/sub/chart.png`). Symlinks are
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
{"path": "files/input.csv", "name": "input.csv", "size": 1280}
```

The field must be named `file`. Use `/v1/files/batch` for multiple files. An
existing file with the same name is replaced. The upload is written through a
size-limited reader, so a lying `Content-Length` cannot fill the disk.

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
{"files": [{"path": "files/a.csv", "name": "a.csv", "size": 10},
           {"path": "files/b.csv", "name": "b.csv", "size": 20}]}
```

The request is bounded by `RUNBOX_MAX_BATCH_FILES` and
`RUNBOX_MAX_REQUEST_BYTES`, and each file by `RUNBOX_MAX_UPLOAD_BYTES`. Files
stream to disk; none are buffered in memory. The batch is not a transaction:
files saved before a later failure remain in `files/`.

## GET /v1/path?path=out/report.csv

Download a file by workspace-relative path, which is what an agent sees when
it runs `ls`:

- `out/report.csv`, `files/photo.jpg`, `./out/x`, and `out\\x` (on Windows
  development) are normalized;
- absolute paths, `..`, drive letters and control characters are rejected;
- the resolved path must remain inside the workspace after following
  symlinks;
- directories are rejected.

The response is `application/octet-stream` with a `Content-Disposition`
header. Range and HEAD requests are supported: send `Range: bytes=0-1023` to
receive `206 Partial Content`, or use `HEAD` to fetch headers without the body.

## DELETE /v1/path?path=out/report.csv

Remove one file or directory from the caller's workspace without running a
command. This is the way out of a workspace that is over its quota, because an
over-quota workspace refuses every command, including `rm`:

```bash
curl -fsS -X DELETE -H "Authorization: Bearer $KEY" \
  -H "X-Runbox-Identity: team-a.user-42" \
  "http://localhost:8099/v1/path?path=out"
```

- accepts the same normalized paths as `GET`; `..` and absolute paths are
  rejected;
- the workspace root itself is refused (`400`), use `DELETE /v1/workspace`;
- another identity's path returns `404`, exactly like `GET`;
- returns `{"deleted":true}`.

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
batch download path for intermediate files; no extra identifiers are required.

## DELETE /v1/workspace

Remove the caller's mutable workspace and release its UID. Any process the
identity left behind is killed first, so the recycled UID cannot inherit it.
The identity is recreated with a fresh UID on the next call.

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
