<p align="center">
  <a href="https://github.com/xy200303/runbox">
    <img src="docs/assets/banner.svg" alt="Runbox — Run in a box. One container, many identities." width="100%">
  </a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="docs/api.md">API</a> ·
  <a href="docs/deployment.md">Deployment</a> ·
  <a href="docs/security.md">Security</a> ·
  <a href="docs/comparison.md">Comparison</a> ·
  <a href="sdk/">SDKs</a> ·
  <a href="https://runbox.xyun.dev">Website</a> ·
  <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <a href="https://github.com/xy200303/runbox/actions/workflows/ci.yml"><img src="https://github.com/xy200303/runbox/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI status"></a>
  <img src="https://img.shields.io/badge/version-0.2.2-2563EB?style=for-the-badge" alt="Version 0.2.2">
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.26">
  <img src="https://img.shields.io/badge/license-MIT-16A34A?style=for-the-badge" alt="MIT license">
  <img src="https://img.shields.io/badge/docker-ready-2496ED?style=for-the-badge&logo=docker&logoColor=white" alt="Docker ready">
  <img src="https://img.shields.io/badge/platform-Linux-0F172A?style=for-the-badge&logo=linux&logoColor=white" alt="Linux">
  <img src="https://img.shields.io/badge/PRs-welcome-34D399?style=for-the-badge" alt="PRs welcome">
</p>

<p align="center">
  <sub><strong>1</strong> server · <strong>1</strong> binary · <strong>1</strong> container · <strong>1</strong> volume · <strong>SQLite</strong> state · <strong>Go / Python / Node</strong> SDKs</sub>
</p>

---

**Runbox** is a lightweight agent sandbox built for a single server. It gives each caller a persistent Linux workspace: upload inputs, run Python, Node or shell commands, and collect the results. Identities share one long-running container, but keep separate UIDs, private workspaces, quotas and path-safe file access.

It is small on purpose: one Go binary, one container, one volume, and a single HTTP API. No scheduler, no control plane, no external database. The name is literal: **run in a box**.

## Project layout

| Path | Contents |
|---|---|
| `cmd/runboxd/` | Runner entrypoint: configuration, signals, graceful shutdown. |
| `internal/server/` | Gin HTTP layer: auth, identity, streaming uploads, Range/HEAD downloads. |
| `internal/runner/` | Workspace isolation, UID credentials, command execution, quotas, cleanup. |
| `internal/store/` | SQLite (GORM) identities, UID assignments and metadata. |
| `internal/config/` | `RUNBOX_*` configuration and advertised limits. |
| `sdk/go/` | Go SDK (package `sandbox`), stdlib-only. |
| `sdk/python/` | Python SDK, stdlib-only, `pip install runbox-sdk`. |
| `sdk/node/` | TypeScript/Node SDK, `npm install runbox-sdk`. |
| `website/` | Product site for [runbox.xyun.dev](https://runbox.xyun.dev). |
| `docs/` | API, architecture, security, deployment, production and migration notes. |
| `examples/` | Runnable Go, Python and curl walkthroughs. |
| `.github/workflows/` | CI, container publishing, SDK releases and GitHub Pages. |

## Why Runbox?

Container-per-sandbox platforms isolate every sandbox in its own container. MicroVM platforms go further with a VM per sandbox. Both are the right trade-off for mutually hostile tenants — and both are heavy on a single machine: one more container or VM per user, often another copy of Python/LibreOffice, and another thing to schedule.

Runbox takes the **dify-sandbox shape**: one long-lived container, many identities. Isolation comes from Linux credentials, file modes, path resolution and quotas — not from a new container per user. On top of that shape, Runbox adds a file isolation layer that a plain per-task UID sandbox does not have.

| | Container per sandbox | MicroVM per sandbox | **Runbox** |
|---|---|---|---|
| Isolation unit | one container per sandbox | one VM per sandbox | UID + private workspace in one container |
| New runtime per identity | yes | yes | **no** |
| Single-machine footprint | control plane + many containers | KVM + runtime + VM images | **one binary, one container, one volume** |
| Cold start per identity | container start or pool checkout | VM boot or snapshot restore | **none** |
| File isolation | volume or API per sandbox | strong | **built-in per identity** |
| Best fit | multi-node, stronger per-sandbox isolation | hostile tenants | **internal or semi-trusted, single machine** |

Choose Runbox when you want one container next to your application, semi-trusted commands, Python/Node/shell workloads, and per-identity file isolation without operating a sandbox platform. If tenants are mutually hostile, run Runbox under gVisor or a microVM instead of treating the shared kernel as a VM boundary.

## Features

| | What you get |
|---|---|
| **Single-machine first** | Designed for one server: no scheduler, no control plane, no external database. SQLite and the workspaces live on the same volume. |
| **One container, many identities** | The HTTP runner and the execution environment share one long-lived container. Each identity runs under its own UID; calls for one identity are serialized, different identities run concurrently. |
| **Per-identity file isolation** | A private workspace, a workspace quota and symlink-safe path resolution. Files are downloaded by workspace-relative path; persistence is the caller's job. |
| **Persistent workspaces** | Installed packages and intermediate files survive across calls. `HOME`, XDG directories and pip/npm prefixes all point inside the workspace and count toward the quota. |
| **Caller-managed persistence** | Files produced under `out/` are returned with their paths and downloadable directly. Runbox does not keep a second long-term copy; download what you need and store it yourself. |
| **Bounded everything** | Upload size, command length, input count, captured stdout (head + tail), output count and size, workspace quota, timeout, concurrency and retention. |
| **Files are first-class** | Upload, execute, list, download by workspace path, delete. Attachments stream as multipart; commands reference files by relative path. |
| **Large files** | Uploads stream to disk without buffering the whole body in memory; downloads support `Range` and `HEAD`. |
| **Batch and paths** | Upload several files at once; download any workspace file by relative path, or stream a directory as a tar archive. Intermediate files need no extra identifier. |
| **Fail-closed hardening** | Read-only root filesystem, dropped capabilities, `no-new-privileges`, tini as PID 1. Startup refuses to run as root without the capabilities it needs. |
| **Observable by default** | `/ready` readiness, Prometheus metrics at `/metrics`, structured text or JSON request logs. |
| **Agent-friendly toolchain** | `git`, `curl`/`wget`, `jq`, `ripgrep`, `fd`, `bat`, `tree`, `file`, `sqlite3`, `less`, `nano`, `lsof` and standard GNU tools are preinstalled. |
| **Small enough to audit** | Gin on the HTTP edge, a stdlib-only SDK, and a small operational footprint. |

## Docker images

Three variants share the same API and run as root inside the container so they
can assign per-identity UIDs:

| Variant | Pull | Download | Use |
|---|---|---|---|
| `full` | `docker pull ghcr.io/xy200303/runbox:latest` | ~600 MB | Office documents, spreadsheets, pandas/numpy, native builds |
| `minimal` | `docker pull ghcr.io/xy200303/runbox:latest-minimal` | ~170 MB | Code, text, PDF extraction |
| `toolbox` | `docker pull ghcr.io/xy200303/runbox:latest-toolbox` | ~285 MB | Compiling without the Office layer |

Versioned tags follow the release, for example `v0.2.2`, `v0.2.2-minimal` and
`v0.2.2-toolbox`.

Run any variant with the same command; only the image tag changes:

```bash
export RUNBOX_API_KEY="$(openssl rand -hex 32)"
docker run -d --name runbox \
  --read-only \
  --tmpfs /tmp:rw,exec,nosuid,size=512m \
  --cap-drop ALL \
  --cap-add CHOWN --cap-add FOWNER --cap-add SETUID --cap-add SETGID \
  --cap-add DAC_OVERRIDE --cap-add KILL \
  --security-opt no-new-privileges:true \
  -p 8099:8099 \
  -e RUNBOX_API_KEY="$RUNBOX_API_KEY" \
  -v runbox-data:/workspace \
  ghcr.io/xy200303/runbox:latest
```

Use `:latest-minimal` for code/text workloads and `:latest-toolbox` when pip or
npm needs to build native extensions.

## Quick start

### 1. Start the runner

```bash
git clone https://github.com/xy200303/runbox.git
cd runbox
cp .env.example .env
# set RUNBOX_API_KEY to a long random value: openssl rand -hex 32
docker compose up -d --build
curl -fsS http://localhost:8099/health
```

```json
{"status":"healthy","version":"dev","uptime_seconds":2}
```

The default image includes LibreOffice, Python and Node. For text/code workloads that do not need Office conversion, build the smaller image:

```bash
docker build --target minimal -t runbox:minimal .
```

### 2. Run a command

```bash
export RUNBOX_API_KEY="$(grep '^RUNBOX_API_KEY=' .env | cut -d= -f2)"

curl -fsS http://localhost:8099/v1/exec \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Namespace: team-a" \
  -H "X-Runbox-Principal: user-42" \
  -H "Content-Type: application/json" \
  -d '{"command":"echo hello from runbox"}'
```

```json
{"exit_code":0,"stdout":"hello from runbox\n","files":[],"duration_ms":3}
```

### 3. Produce and fetch a file

```bash
curl -fsS http://localhost:8099/v1/exec \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Namespace: team-a" \
  -H "X-Runbox-Principal: user-42" \
  -H "Content-Type: application/json" \
  -d '{"command":"mkdir -p out && echo hello > out/report.txt"}'
```

The response lists `out/report.txt` with its workspace-relative path. Download it with `GET /v1/path?path=out/report.txt`.

### 4. Or run from source

Windows, macOS and Linux are supported for development. UID isolation is Linux-only.

```bash
make run
# or
RUNBOX_ALLOW_ANONYMOUS=1 RUNBOX_ROOT=./.runbox go run ./cmd/runboxd
```

## Use from Go

```go
import (
    "context"
    "os"
    "time"

    "github.com/xy200303/runbox/sdk/go"
)

client, err := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL:  "http://localhost:8099",
    APIKey:   os.Getenv("RUNBOX_API_KEY"),
    Identity: sandbox.ScopedIdentity("team-a", "user-42"),
})
if err != nil {
    return err
}

result, err := client.Run(context.Background(), sandbox.ExecRequest{
    Command: `python3 -c "print(6 * 7)"`,
    Timeout: 30 * time.Second,
})
if err != nil {
    return err
}

// result.ExitCode is a normal result: the command ran and chose to exit.
fmt.Println(result.Stdout)
for _, file := range result.Files {
    out, err := os.Create(filepath.Base(file.Name))
    if err != nil {
        return err
    }
    if _, err := client.DownloadPathTo(context.Background(), sandbox.Identity{}, file.Path, out); err != nil {
        out.Close()
        return err
    }
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

`Client` also implements `sandbox.Backend`: use `Exec(ctx, identity, …)` when the identity is passed per call, or `Run` with a client bound to one identity.

## Python and Node SDKs

Python is stdlib-only:

```bash
pip install runbox-sdk
```

```python
from runbox import Client, Identity

client = Client(
    "http://localhost:8099",
    api_key=os.environ["RUNBOX_API_KEY"],
    identity=Identity(namespace="team-a", principal="user-42"),
)
result = client.exec('python3 -c "print(6 * 7)"')
print(result["stdout"])
client.download_path("out/report.csv", "report.csv")
client.download_archive(["out", "tmp"], "artifacts.tar")
```

Node ships typed ESM:

```bash
npm install runbox-sdk
```

```ts
import { Client } from 'runbox-sdk'

const client = new Client({
  baseUrl: 'http://localhost:8099',
  apiKey: process.env.RUNBOX_API_KEY,
  identity: { namespace: 'team-a', principal: 'user-42' },
})
const result = await client.exec('echo hello')
await client.downloadArchive(['out'], 'artifacts.tar')
```

Source: [`sdk/python`](sdk/python) and [`sdk/node`](sdk/node). A full curl
walkthrough — upload, exec, list, download — is in
[`examples/curl.sh`](examples/curl.sh).

## SDK integration rules

Every SDK follows the same contract:

1. **Base URL** points at `runboxd`, for example `http://localhost:8099`.
2. **Authentication** is `Authorization: Bearer <RUNBOX_API_KEY>`.
3. **Identity** is exactly one form: `X-Runbox-Identity`, or
   `X-Runbox-Namespace` + `X-Runbox-Principal`.
4. **Files are paths.** Every path is relative to that identity's workspace;
   there are no file IDs to translate.
5. **Uploads** return paths: `POST /v1/files` with field `file`, or
   `POST /v1/files/batch` with repeated `files`.
6. **Exec attachments** use repeated `files` fields and land in `files/`; the
   command refers to them as `files/<name>`.
7. **Downloads** use `GET /v1/path?path=...` for one file and
   `GET /v1/archive?path=...&path=...` for a batch tar. The SDKs expose these as
   `OpenPath`/`DownloadPathTo`, `download_path`, `downloadPath`, and
   `DownloadArchive`/`download_archive`/`downloadArchive`.

## Identity model

Every request carries exactly one identity form:

| Mode | Headers | Use when |
|---|---|---|
| Single token | `X-Runbox-Identity: <token>` | Your application already mints an opaque subject ID. |
| Scoped | `X-Runbox-Namespace: <namespace>`<br>`X-Runbox-Principal: <principal>` | You need tenant + user, and want metadata for tenant-level operations later. |

Rules:

- Supplying both forms, or only half of the scoped form, returns `400`.
- Tokens are 1–256 visible ASCII characters with no spaces.
- Namespace and principal are 1–128 characters from `[A-Za-z0-9._-]`.
- Both forms normalize to one canonical SHA-256 key; storage paths use that key.
- Raw tokens are never written to disk. Scoped namespace/principal and the UID assignment are stored in the `identities` table of `state/runbox.db`.
- Paths are resolved relative to the identity workspace. Another identity sees its own workspace, or `404`.

Authentication is `Authorization: Bearer <RUNBOX_API_KEY>`. `/health` and `/ready` are unauthenticated; everything else requires the bearer token.

## API at a glance

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Liveness, version, uptime (no auth) |
| GET | `/ready` | Readiness: storage directories exist and are writable (no auth) |
| GET | `/metrics` | Prometheus metrics (auth required) |
| GET | `/v1/capabilities` | Limits and detected tooling |
| POST | `/v1/exec` | Run a command (JSON or multipart) |
| POST | `/v1/files` | Upload one file; returns its path |
| POST | `/v1/files/batch` | Upload several files; returns their paths |
| GET | `/v1/path?path=out/x` | Download by workspace path |
| GET | `/v1/list?path=out` | List a workspace directory |
| GET | `/v1/archive?path=out&path=tmp` | Download files or directories as a tar archive |
| DELETE | `/v1/workspace` | Remove the workspace |

Full request and response reference: [docs/api.md](docs/api.md).

## Batch download

Download a directory or several paths as one tar stream:

```bash
curl -fsS \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  "http://localhost:8099/v1/archive?path=out&path=tmp/step.txt" \
  -o artifacts.tar
```

SDKs expose the same entry as `DownloadArchive` (Go), `download_archive`
(Python) and `downloadArchive` (Node). No file IDs are involved; paths are
relative to the caller's workspace.

## How it works

```mermaid
flowchart LR
    A["Agent or application"] -->|"HTTP · Bearer · X-Runbox-*"| B["runboxd"]
    B -->|"fork/exec"| C["/bin/sh -c command · UID 20000+n"]
    B --> D[("workspaces/identity-key · mutable")]
```

```text
RUNBOX_ROOT/
├── workspaces/<identity-key>/       0750, owned by the identity's UID
│   ├── files/                       uploaded inputs
│   ├── out/                         files returned from the last command
│   ├── .local/  .npm-global/        persistent pip / npm installs
│   ├── .cache/  .config/  .tmp/     persistent XDG state and temp files
│   └── ...
└── state/
    ├── runbox.db                    identities, UIDs and metadata (SQLite)
    ├── runbox.db-wal                WAL journal while the runner is active
    └── runbox.db-shm                shared-memory index while the runner is active
```

- The command runs in its own process group as the identity's UID. A timeout kills the group, not the container.
- Output is captured with a bounded head + tail buffer, so a command printing gigabytes cannot exhaust RAM.
- Files created under `out/` during the call are returned with their workspace-relative paths. Download them by path; Runbox keeps no second copy.
- A janitor removes workspaces idle longer than the retention window.
- The API key is compared in constant time; identity values are validated, not sanitized — sanitizing would merge distinct identities onto one workspace.

More detail: [docs/architecture.md](docs/architecture.md).

## Security

- Path resolution rejects `..`, absolute paths, Windows drive letters and symlink escapes. Serving a symlink planted in the workspace would otherwise read files as root.
- Commands never inherit the runner's environment, including the API key.
- The compose file runs read-only with `cap_drop: ALL`, adds only `CHOWN`, `FOWNER`, `SETUID`, `SETGID`, `DAC_OVERRIDE` and `KILL`, sets `no-new-privileges`, and caps memory, CPU and pids.
- Images run tini as PID 1 so killed command trees are reaped.
- Startup fails closed when running as root without the required capabilities. `RUNBOX_ALLOW_MISSING_CAPS=1` is a development-only override.

> Runbox is not a strong multi-tenant boundary on its own. It relies on the container's namespaces/cgroups plus per-UID file permissions. For hostile tenants, run it under gVisor or a microVM. See [docs/security.md](docs/security.md).

## Operations

- `/ready` verifies that the data directories exist and the state directory is writable.
- `/metrics` exposes bounded Prometheus counters: requests, execs, timeouts, busy, upload/download bytes, workspace deletes, active commands and uptime.
- Graceful shutdown cancels in-flight commands after the configured budget and kills their process groups.
- Logs are structured text or JSON (`RUNBOX_LOG_FORMAT`).

See [docs/production.md](docs/production.md) for the production checklist: SLOs, capacity, quotas, key handling, gVisor, multi-replica and runbooks.

<details>
<summary><strong>All configuration</strong></summary>

Durations accept `30s`, `15m`, `7d` or bare seconds; sizes accept `20MiB`, `20MB`, `1GiB` or bytes.

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
| `RUNBOX_MAX_ATTACHMENTS` | `64` | Maximum multipart attachments per execution. |
| `RUNBOX_MAX_STDOUT_BYTES` | `200KiB` | Captured stdout+stderr limit; keeps the head and the tail. |
| `RUNBOX_MAX_OUTPUT_FILES` | `20` | Files returned from one execution. |
| `RUNBOX_MAX_OUTPUT_BYTES` | `200MiB` | Total output size returned from one execution. |
| `RUNBOX_MAX_BATCH_FILES` | `1000` | Files per batch upload or tar archive. |
| `RUNBOX_MAX_BATCH_BYTES` | `1GiB` | Total size of one tar archive download. |
| `RUNBOX_QUOTA_BYTES` | `1GiB` | Per-workspace quota; execution is refused above it. `0` = unlimited. |
| `RUNBOX_RETENTION` | `7d` | Idle workspace retention. |
| `RUNBOX_SWEEP_INTERVAL` | `1h` | Janitor interval. |
| `RUNBOX_UID_MIN` / `RUNBOX_UID_MAX` | `20000` / `20100` | UID pool used for Linux isolation. |
| `RUNBOX_DEFAULT_TIMEOUT` | `60s` | Timeout when the request omits one. |
| `RUNBOX_MAX_TIMEOUT` | `300s` | Upper bound; longer requests are clamped. |
| `RUNBOX_SHUTDOWN_TIMEOUT` | `30s` | Graceful shutdown budget. |
| `RUNBOX_TLS_CERT` / `RUNBOX_TLS_KEY` | — | Terminate TLS in-process. A reverse proxy is usually better. |
| `RUNBOX_LOG_FORMAT` | `text` | `text` or `json`. |

</details>

## Image contents

| Capability | Full | Minimal | Toolbox |
|---|---|---|---|
| Agent CLI (git, curl/wget, jq, ripgrep, fd, bat, tree, file, sqlite3, less, nano, lsof) | yes | yes | yes |
| Python 3, Node, npm, unzip/zip | yes | yes | yes |
| `pdftotext` | yes | yes | yes |
| LibreOffice (Writer/Calc/Impress) | yes | no | no |
| CJK fonts | yes | no | no |
| Python document/data libraries | yes | no | no |
| Node `xlsx`, `csv-parse`, `dayjs` | yes | no | no |
| `build-essential` and development headers | yes | no | yes |

Sizes above are the amd64 download size; unpacking takes roughly three to four
times that on disk (`docker images` reports about 700 MB for `minimal`). `full`
now includes everything in `toolbox`, so pick `minimal` for code/text workloads
and `toolbox` only when you want the compiler without the Office layer.

Build arguments for restricted networks: `GOPROXY`, `APT_MIRROR`, `PIP_INDEX`, `NPM_REGISTRY`. See [docs/deployment.md](docs/deployment.md).

## Comparison

| | Runbox | Container-per-sandbox (OpenSandbox / CubeSandbox) | MicroVM (E2B / microsandbox) |
|---|---|---|---|
| Isolation unit | UID + private workspace in one container | one container (or pool slot) per sandbox | one VM per sandbox |
| New container/VM per user | no | yes | yes |
| Single-machine footprint | one binary, one container, one volume | control plane + many containers | KVM + runtime + VM images |
| Startup per identity | no container/VM boot | container start or pool checkout | VM boot or snapshot restore |
| File isolation | built-in: workspace, quota, symlink-safe paths | volume or API per sandbox | strong |
| Best fit | internal/semi-trusted, single machine | multi-node, stronger per-sandbox isolation | hostile tenants |

OpenSandbox and CubeSandbox are control-plane projects and support different deployment modes; the column describes the common container-per-sandbox model, not a claim about every mode. See [docs/comparison.md](docs/comparison.md) for gVisor, Dify and the full trade-off list.

The closest prior art is **dify-sandbox**: one container with a per-task UID. Runbox keeps that shape and adds the per-identity file isolation described above.

## FAQ

<details>
<summary><strong>Does every user get a container?</strong></summary>

No. Every identity shares one long-lived container and gets a UID plus a private workspace inside it. That is what makes single-machine deployment practical.

</details>

<details>
<summary><strong>Can I run untrusted code?</strong></summary>

Not as a hard multi-tenant boundary. Runbox uses the container's namespaces and cgroups plus per-UID file permissions, so the kernel is shared. For hostile tenants, run it under gVisor or a microVM.

</details>

<details>
<summary><strong>How is it different from Dify's sandbox?</strong></summary>

Dify's sandbox is part of the Dify stack and scopes files per task. Runbox is standalone, exposes a stable HTTP contract, and adds per-identity workspaces, quotas and symlink-safe paths.

</details>

<details>
<summary><strong>Does it work on Windows or macOS?</strong></summary>

For development, yes. Production isolation is Linux-only, because per-identity UID isolation depends on Linux credentials and file ownership.

</details>

## Roadmap

- [ ] Read-only usage endpoint: per-namespace identity count, workspace bytes and in-flight commands
- [x] SBOM and provenance attestations for published images
- [ ] Signed container images with cosign/Sigstore

Egress filtering, filesystem-level disk quotas, seccomp profiles and multi-replica
routing are deployment decisions, not runner features. Runbox documents how to
handle each one in [docs/deployment.md](docs/deployment.md) and
[docs/production.md](docs/production.md) rather than building it into the daemon.

## Contributing

Issues and pull requests are welcome. Before opening a PR, run:

```bash
make check   # gofmt, go vet, go test -race
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow and [SECURITY.md](SECURITY.md) for reporting security issues.

## License

MIT. See [LICENSE](LICENSE).

<p align="center">
  <sub><strong>Run in a box.</strong> If Runbox is useful to you, a star helps others find it.</sub>
</p>
