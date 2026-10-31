# Runbox

**Run in a box.**

One box for agent code and files — a lightweight, single-container execution sandbox for AI agents.

Runbox gives an agent a disposable Linux workspace: upload inputs, run Python, Node or shell commands, download the artifacts. It does not host agents or run a model loop; it is the execution and file backend the agent calls. One Go binary, one container, one volume — no `docker.sock`, no orchestrator, no database. The name and slogan are literal: **run in a box**, one box for many identities.

[中文说明](README.zh-CN.md)

- **One container, many identities.** Runbox does not create a container per user. The HTTP runner and the execution environment share one long-lived container; each identity runs under its own UID and gets a private workspace.
- **Per-identity isolation, including files.** Each identity gets a workspace, a quota and an immutable archive outside the writable tree. Calls for one identity are serialized; different identities run concurrently.
- **Bounded everything.** Upload size, captured stdout (head+tail, memory-bounded), output count and size, workspace quota, archive quota, command timeout, concurrency and retention.
- **Files are first-class.** Upload, execute with inputs, list, download by opaque ID or workspace path, and archive an immutable copy outside the user-writable workspace.
- **Safe path handling.** Absolute paths, `..`, drive letters and symlink escapes are rejected; symlinks are not collected as outputs.
- **No secret leakage.** Commands receive a minimal environment; the runner's API key and process environment are not inherited.
- **Stdlib-only Go SDK.** JSON for plain calls, streaming multipart when files are attached.

> Runbox is not a strong multi-tenant boundary on its own. It relies on the container's namespaces/cgroups plus per-UID file permissions. For hostile tenants, run it under gVisor or a microVM. See [docs/security.md](docs/security.md).

## Why Runbox

Container-per-sandbox platforms (the common deployment of OpenSandbox and
CubeSandbox, for example) isolate each sandbox in its own container; microVM
platforms (E2B, microsandbox) go further with a VM per sandbox. That is the
right trade-off for mutually hostile tenants, but on a single machine it means
one more container or VM — and often another copy of Python/LibreOffice — per
user or task.

Runbox follows the dify-sandbox shape instead: one long-lived container, many
identities. Each identity gets a stable UID and a private workspace inside that
container; isolation comes from Linux credentials, file modes, path resolution
and quotas, not from a new container per user. On top of that shape Runbox adds
a file isolation layer: per-identity workspace, per-identity quota, immutable
`keep/` outside the writable tree, symlink-safe reads and a bounded output
channel.

The result is a single-machine-friendly deployment: one binary, one container,
one volume, no scheduler. Choose it for an internal agent platform or a SaaS
with semi-trusted users. When tenants are mutually hostile, run Runbox under
gVisor or one container per trust domain; do not treat the shared kernel as a
microVM boundary.

### The name

**Runbox** is literal: *run in a box*. The slogan is **Run in a box.** The box
runs agent code and holds agent files; it does not run the agent itself. The box
is the single container, and identities are the many callers inside it. It is
deliberately not one container per user, and not one VM per sandbox.

## Quick start

```bash
cp .env.example .env
# edit .env and set a long RUNBOX_API_KEY (openssl rand -hex 32)
docker compose up -d --build
curl -fsS http://localhost:8099/health
```

The default image includes LibreOffice, Python and Node. A smaller image without LibreOffice:

```bash
docker build --target minimal -t runbox:minimal .
```

Run locally (Windows, macOS or Linux; UID isolation only on Linux):

```bash
make run
# or
RUNBOX_ALLOW_ANONYMOUS=1 RUNBOX_ROOT=./.runbox go run ./cmd/runboxd
```

## API in one table

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Liveness, version, uptime (no auth) |
| GET | `/ready` | Readiness: storage directories exist and are writable (no auth) |
| GET | `/metrics` | Prometheus metrics (auth required) |
| GET | `/v1/capabilities` | Limits and detected tooling |
| POST | `/v1/exec` | Run a command (JSON or multipart) |
| POST | `/v1/files` | Upload one file |
| GET | `/v1/files/{id}` | Download a file by ID |
| POST | `/v1/files/{id}/keep` | Archive a file immutably |
| GET | `/v1/path?path=out/x` | Download by workspace path |
| GET | `/v1/list?path=out` | List a workspace directory |
| DELETE | `/v1/workspace[?purge=true]` | Remove the workspace (and optionally the archive) |

Identity (one of):

```http
X-Runbox-Identity: 01J...
```

or:

```http
X-Runbox-Namespace: tenant-a
X-Runbox-Principal: user-42
```

Supplying both forms is rejected. Authentication: `Authorization: Bearer <key>`.

Full request/response reference: [docs/api.md](docs/api.md).

## Go SDK

```go
client, _ := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL:   "http://localhost:8099",
    APIKey:    os.Getenv("RUNBOX_API_KEY"),
    Identity: sandbox.ScopedIdentity("team-a", "user-42"),
})
result, err := client.Run(ctx, sandbox.ExecRequest{
    Command: `python3 -c "open('out/report.csv','w').write('a,b\n1,2\n')"`,
    Timeout: 30 * time.Second,
})
for _, file := range result.Files {
    out, _ := os.Create(filepath.Base(file.Name))
    client.DownloadTo(ctx, file.FileID, out)
    out.Close()
}
```

Attachments stream straight from an `io.Reader`:

```go
result, err := client.Run(ctx, sandbox.ExecRequest{
    Command: "python3 analyze.py files/input.csv",
    Files:   []sandbox.FileUpload{{Name: "input.csv", Reader: file}},
})
```

`Identity` supports either a single token (`sandbox.TokenIdentity`) or a
namespace + principal (`sandbox.ScopedIdentity`). Both normalize to one
canonical storage key; namespace/principal are kept as metadata for tenant-level
operations.

`Client` also implements `sandbox.Backend`: use `Exec(ctx, identity, …)` when
the identity is passed per call, or `Run` with a client bound to one identity.

Full example: [examples/go](examples/go). Stdlib-only Python client: [examples/python](examples/python). curl walkthrough: [examples/curl.sh](examples/curl.sh).

## Configuration

Every setting is an environment variable. Durations accept `30s`, `15m`, `7d` or bare seconds; sizes accept `20MiB`, `20MB`, `1GiB` or bytes.

| Variable | Default | Meaning |
|---|---|---|
| `RUNBOX_API_KEY` | — (required) | Shared API key. Startup fails without it unless anonymous mode is on. |
| `RUNBOX_ALLOW_ANONYMOUS` | `false` | Disable authentication. Local development only. |
| `RUNBOX_ALLOW_MISSING_CAPS` | `false` | Allow root startup without the Linux capabilities Runbox needs. Development only; production fails closed. |
| `RUNBOX_ADDR` | `:8099` | Listen address. |
| `RUNBOX_ROOT` | `.runbox` | Data root. The image sets `/workspace`. |
| `RUNBOX_MAX_CONCURRENCY` | `2` | Concurrent commands per runner. Excess calls get `409 busy`. |
| `RUNBOX_MAX_REQUEST_BYTES` | `200MiB` | HTTP body limit. |
| `RUNBOX_MAX_UPLOAD_BYTES` | `20MiB` | Per-file limit. |
| `RUNBOX_MAX_COMMAND_BYTES` | `64KiB` | Maximum command length. |
| `RUNBOX_MAX_INPUTS` | `64` | Maximum inputs (file IDs plus multipart files) per execution. |
| `RUNBOX_MAX_STDOUT_BYTES` | `200KiB` | Captured stdout+stderr limit; keeps the head and the tail. |
| `RUNBOX_MAX_OUTPUT_FILES` | `20` | Files returned from one execution. |
| `RUNBOX_MAX_OUTPUT_BYTES` | `200MiB` | Total output size returned from one execution. |
| `RUNBOX_QUOTA_BYTES` | `1GiB` | Per-workspace quota; execution is refused above it. `0` = unlimited. |
| `RUNBOX_KEEP_BYTES` | `1GiB` | Per-identity archive quota. `0` = unlimited. |
| `RUNBOX_RETENTION` | `7d` | Idle workspace retention. |
| `RUNBOX_SWEEP_INTERVAL` | `1h` | Janitor interval. |
| `RUNBOX_UID_MIN` / `RUNBOX_UID_MAX` | `20000` / `20100` | UID pool used for Linux isolation. |
| `RUNBOX_DEFAULT_TIMEOUT` | `60s` | Timeout when the request omits one. |
| `RUNBOX_MAX_TIMEOUT` | `300s` | Upper bound; longer requests are clamped. |
| `RUNBOX_SHUTDOWN_TIMEOUT` | `30s` | Graceful shutdown budget. |
| `RUNBOX_TLS_CERT` / `RUNBOX_TLS_KEY` | — | Terminate TLS in-process. A reverse proxy is usually better. |
| `RUNBOX_LOG_FORMAT` | `text` | `text` or `json`. |

## How it works

```
client ──HTTP──► runboxd ──fork/exec──► /bin/sh -c "…"  (UID 20000+n)
                    │                        │
                    ├─ /workspace/workspaces/<identity-key>/  mutable, user-owned
                    └─ /workspace/keep/<identity-key>/        immutable, runner-owned
```

- One workspace per identity, persisted across calls, so installed packages and intermediate files survive.
- Identity is either a token or namespace+principal; both normalize to one canonical key. Scoped namespace/principal is kept as metadata for tenant-level operations; tokens are never persisted.
- `HOME`, `XDG_*`, pip and npm prefixes point inside the workspace; installs persist and count toward the quota.
- The command runs in its own process group as the identity's UID; a timeout kills the group, not the container.
- Output is capped with a head+tail buffer, so a command printing gigabytes cannot exhaust RAM.
- Files created under `out/` during the call are returned as file IDs. Delivered files are copied into `keep/`, outside the writable workspace, so later commands cannot alter them.
- A janitor removes workspaces idle longer than the retention window; `keep/` is untouched.

More detail: [docs/architecture.md](docs/architecture.md).

## Security

- Path resolution rejects `..`, absolute paths, Windows drive letters and symlink escapes. Serving a symlink planted in the workspace would otherwise read files as root.
- Commands never inherit the runner's environment.
- The compose file runs read-only with `cap_drop: ALL`, adds only `CHOWN`, `FOWNER`, `SETUID`, `SETGID`, `DAC_OVERRIDE` and `KILL`, sets `no-new-privileges`, and caps memory, CPU and pids. Images run tini as PID 1 to reap killed command trees.
- The API key is compared in constant time; identity values are validated, not sanitized (sanitizing would merge distinct identities onto one workspace).
- Archived files are runner-owned and live outside the workspace; the sandbox identity cannot rewrite a delivered file.

Known limits and the isolation upgrade path (gVisor, microVM, network policy): [docs/security.md](docs/security.md).

## Production

`/ready` verifies that the data directories exist and the state directory is
writable. `/metrics` exposes bounded Prometheus counters (requests, execs,
timeouts, busy, bytes, archives, active commands). Startup fails closed when the
process is root and `CHOWN`, `FOWNER`, `SETUID`, `SETGID`, `DAC_OVERRIDE` or
`KILL` is missing.

See [docs/production.md](docs/production.md) for the commercial checklist:
SLOs, capacity, quotas, key handling, gVisor, multi-replica and runbooks.

## Image contents

| Capability | Full | Minimal |
|---|---|---|
| Python 3, Node, npm, git, curl, unzip/zip | yes | yes |
| `pdftotext` | yes | yes |
| LibreOffice (Writer/Calc/Impress) | yes | no |
| CJK fonts | yes | no |
| Python document/data libraries | yes | no |
| Node `xlsx`, `csv-parse`, `dayjs` | yes | no |

Build arguments for restricted networks: `GOPROXY`, `APT_MIRROR`, `PIP_INDEX`, `NPM_REGISTRY` (see [docs/deployment.md](docs/deployment.md)).

## Comparison

| | Runbox | Container-per-sandbox (OpenSandbox / CubeSandbox) | MicroVM (E2B / microsandbox) |
|---|---|---|---|
| Isolation unit | UID + private workspace in one container | one container (or pool slot) per sandbox | one VM per sandbox |
| New container/VM per user | no | yes | yes |
| Single-machine footprint | one binary, one container, one volume | control plane + many containers | KVM + runtime + VM images |
| Startup per identity | no container/VM boot | container start or pool checkout | VM boot or snapshot restore |
| File isolation | built-in: workspace, quota, immutable archive, symlink-safe | volume or API per sandbox | strong |
| Best fit | internal/semi-trusted, single machine | multi-node, stronger per-sandbox isolation | hostile tenants |

OpenSandbox and CubeSandbox are control-plane projects and support different
deployment modes; the column describes the common container-per-sandbox model,
not a claim about every mode. See [docs/comparison.md](docs/comparison.md) for
gVisor, Dify and the full trade-off list.

The closest prior art is **dify-sandbox**: one container with a per-task UID.
Runbox keeps that shape and adds the per-identity file isolation described
above.

## Limitations / roadmap

- No built-in egress control; use a network policy or firewall if commands must not reach the internet.
- No HTTP range requests; large downloads stream from the start.
- Workspaces are local to one runner. Multiple replicas need sticky routing per identity or shared storage.
- No custom seccomp profile beyond Docker's defaults; gVisor is the recommended next step.
- `keep/` is quota-limited per identity but does not expire automatically.

## License

MIT.
