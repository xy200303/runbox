# Deployment

## Image variants

| Target | Contents | Rough size | Use |
|---|---|---|---|
| `full` (default) | Debian 12 slim, agent CLI, Python/Node, poppler, `build-essential`, LibreOffice, CJK fonts, data libraries | ~600 MB download | Document work and native builds |
| `minimal` | Debian 12 slim, agent CLI, Python/Node, poppler; no LibreOffice/CJK/data libraries | ~170 MB download | Code/text workloads |
| `toolbox` | minimal plus `build-essential` and development headers | ~285 MB download | Compilation only, no LibreOffice |

The sizes are amd64 download sizes from the published registry manifests;
unpacked footprints are roughly three to four times larger. `full` is the
default because document conversion is the common agent workload; use
`minimal` when size matters.

### Why Debian slim

Debian 12 slim is the base because it keeps the three things that matter for an
agent sandbox:

- **glibc and the standard Linux layout.** Manylinux Python wheels and native
  Node modules work without musl workarounds; paths, `bash` and GNU tools match
  what agents expect.
- **apt with a large package set.** LibreOffice, poppler, Python, Node and the
  common CLI tools are one `apt-get install` away. `--no-install-recommends`
  and removing `/var/lib/apt/lists` keep the layer small.
- **A small, stable base.** `debian:bookworm-slim` is roughly 75 MB before the
  tools, is multi-arch, and has a predictable security-update cadence. Docker
  Official Images for Python, Node and Postgres also publish Debian variants,
  so the base is familiar to operators.

Ubuntu is a close alternative (newer packages, also Debian-derived); Alpine is
smaller but musl breaks too many Python/Node binaries, and distroless/scratch
have no shell or apt, which is the opposite of an agent sandbox. The image size
is dominated by the tools, not the base: `minimal` downloads ~170 MB and
`full` ~600 MB.

```bash
docker build -t runbox:latest .
docker build --target minimal -t runbox:minimal .
```

Prebuilt multi-arch images are published by the `docker` workflow:

```text
ghcr.io/xy200303/runbox:latest
ghcr.io/xy200303/runbox:latest-minimal
ghcr.io/xy200303/runbox:v0.2.3
ghcr.io/xy200303/runbox:v0.2.3-minimal
ghcr.io/xy200303/runbox:v0.2.3-toolbox
```

Pin a version tag in production; do not rely on `latest`.

### Restricted networks

All build stages accept mirrors:

```bash
docker build \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --build-arg APT_MIRROR=mirrors.aliyun.com \
  --build-arg PIP_INDEX=https://pypi.tuna.tsinghua.edu.cn/simple \
  --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
  -t runbox:latest .
```

For Docker Hub rate limits, configure a registry mirror in
`/etc/docker/daemon.json`, for example:

```json
{"registry-mirrors": ["https://registry.cn-hangzhou.aliyuncs.com"]}
```

## Docker

```bash
docker run -d --name runbox \
  -p 127.0.0.1:8099:8099 \
  -e RUNBOX_API_KEY="$(openssl rand -hex 32)" \
  -e RUNBOX_MAX_CONCURRENCY=2 \
  -v runbox-workspace:/workspace \
  --read-only \
  --tmpfs /tmp:rw,exec,nosuid,size=1g \
  --cap-drop ALL \
  --cap-add CHOWN --cap-add FOWNER --cap-add SETUID --cap-add SETGID --cap-add DAC_OVERRIDE --cap-add KILL \
  --security-opt no-new-privileges:true \
  --memory 2g --cpus 2 --pids-limit 256 \
  --restart unless-stopped \
  runbox:latest
```

The bind address `127.0.0.1` is deliberate: expose the runner through a
gateway, not the public internet.

## Docker Compose

```bash
cp .env.example .env
# set RUNBOX_API_KEY
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8099/health
curl -fsS http://127.0.0.1:8099/ready
```

The image runs tini as PID 1 and the compose file grants `KILL`. Keep both:
without `KILL`, a timeout returns but the command keeps running as the
identity's UID; without a reaping init, killed descendants accumulate as
zombies.

The repository's `docker-compose.yml` includes the read-only root, tmpfs,
capability drop, resource limits and health check.

## TLS and reverse proxy

Prefer a reverse proxy so certificate reloads and access policy stay outside
the runner.

Nginx:

```nginx
server {
    listen 443 ssl;
    server_name sandbox.example.com;
    ssl_certificate     /etc/ssl/fullchain.pem;
    ssl_certificate_key /etc/ssl/privkey.pem;

    client_max_body_size 200m;

    location / {
        proxy_pass http://127.0.0.1:8099;
        proxy_http_version 1.1;
        proxy_request_buffering off;   # stream multipart uploads
        proxy_read_timeout 400s;      # > RUNBOX_MAX_TIMEOUT
        proxy_send_timeout 400s;
        proxy_set_header X-Request-Id $request_id;
    }
}
```

Caddy:

```caddyfile
sandbox.example.com {
    request_body {
        max_size 200MB
    }
    reverse_proxy 127.0.0.1:8099 {
        transport http {
            read_timeout 400s
            write_timeout 400s
        }
    }
}
```

If the gateway authenticates end users, set exactly one identity form there
(`X-Runbox-Identity`, or `X-Runbox-Namespace` + `X-Runbox-Principal`) and strip
any client-supplied identity headers. Otherwise a caller with the shared bearer
token can choose any identity; see [security.md](security.md).

In-process TLS is also available:

```bash
RUNBOX_TLS_CERT=/certs/fullchain.pem RUNBOX_TLS_KEY=/certs/privkey.pem runboxd
```

## Kubernetes

Runbox needs to run as root and hold `CHOWN`, `FOWNER`, `SETUID`, `SETGID` and
`DAC_OVERRIDE` to assign per-identity UIDs. Everything else can stay locked
down:

```yaml
spec:
  securityContext:
    runAsUser: 0
  containers:
    - name: runbox
      image: registry.example.com/runbox:0.2.0
      env:
        - name: RUNBOX_API_KEY
          valueFrom: {secretKeyRef: {name: runbox, key: api-key}}
        - name: RUNBOX_ROOT
          value: /workspace
      ports:
        - {name: http, containerPort: 8099}
      securityContext:
        readOnlyRootFilesystem: true
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
          add: [CHOWN, FOWNER, SETUID, SETGID, DAC_OVERRIDE, KILL]
        seccompProfile:
          type: RuntimeDefault
      resources:
        requests: {cpu: "500m", memory: 512Mi}
        limits:   {cpu: "2",    memory: 2Gi}
      volumeMounts:
        - {name: workspace, mountPath: /workspace}
        - {name: tmp, mountPath: /tmp}
  volumes:
    - name: workspace
      persistentVolumeClaim: {claimName: runbox-workspace}
    - name: tmp
      emptyDir: {medium: Memory, sizeLimit: 1Gi}
```

- Set `pidsLimit` through the container runtime; Kubernetes does not expose it
  directly on the Pod spec.
- The image runs tini as PID 1, so no extra init container is needed for
  reaping. Keep `KILL` in the capability set so timeouts can terminate
  commands running as another UID.
- The workspace PVC must be `ReadWriteOnce`; a runner is a single writer.
- Multiple replicas need sticky routing per identity or one PVC per replica.
  There is no cross-replica workspace sharing.

## Egress and disk limits

Runbox executes commands as they are. It does not filter outbound traffic and
does not impose filesystem quotas; both belong to the layer that owns the host.

- **Egress.** On a single host, terminate it at the network layer: drop outbound
  traffic from the container's bridge and allow only the package mirrors the
  toolchains need, or force the container through a proxy with `HTTP_PROXY`,
  `HTTPS_PROXY` and `NO_PROXY`. On Kubernetes use a `NetworkPolicy`.
  Per-identity egress rules are not available: every identity shares one
  network namespace, so the container is the unit of egress control.
- **Disk.** Bind-mount a dedicated volume at `RUNBOX_ROOT` and size it for
  `RUNBOX_QUOTA_BYTES * active identities`. `RUNBOX_QUOTA_BYTES` is a
  pre-execution check that returns `409 quota_exceeded`; it never interrupts a
  running command. For a hard limit, enable project quotas on the host
  filesystem (for example `prjquota` on XFS) and assign each identity
  subdirectory a project.

## Sizing

| Resource | Guidance |
|---|---|
| CPU | Each execution is one shell plus its children. LibreOffice and pandas are the heavy paths. `2` CPUs with concurrency `2` is a reasonable start. |
| Memory | Container limit `2 GiB` handles two small Python jobs or one LibreOffice conversion with headroom. Raise it before raising concurrency. |
| Disk | Image ~600 MB download (full) or ~170 MB (minimal), roughly 3–4× that unpacked. Workspace volume: `quota * active identities`. |
| pids | `256` stops fork bombs early while allowing LibreOffice helper processes. |
| Concurrency | Start at `2`. Watch `busy` responses and CPU throttling; scale out with more replicas rather than raising it indefinitely. |

## Monitoring

- `/health` for liveness (no auth).
- `/ready` for readiness: it checks the data directories and state-dir
  writability (no auth). Use this for orchestrator probes.
- `/metrics` for Prometheus text metrics (auth required; configure the scraper
  with `Authorization: Bearer <RUNBOX_API_KEY>`).
- `/v1/capabilities` for limits and detected tools (auth required).
- Logs are JSON when `RUNBOX_LOG_FORMAT=json`; each request line includes
  `request_id`, method, path, status, bytes and duration.
- There are no Prometheus metrics yet. Derive rates from logs (status 409
  `busy`, 409 `quota_exceeded`, 413, timeouts, p95 duration).
- Alert on disk usage, memory pressure, container restarts and a rising `busy`
  rate.

## Upgrades

1. Build and test the new image.
2. Recreate the container (`docker compose up -d --force-recreate`). On
   shutdown, Runbox waits for in-flight requests up to
   `RUNBOX_SHUTDOWN_TIMEOUT`, then cancels them and kills their process groups.
3. Workspaces stay on the volume. UID assignments are re-derived from the
   identity key; files keep their numeric owner, so ownership remains
   consistent for existing identities.
4. Recreating the container also clears runtime state installed outside the
   workspace (for example `/tmp` and `/usr`). Doing this daily is a cheap way
   to shed leftovers from compromised or messy commands.

Changing `RUNBOX_UID_MIN`/`RUNBOX_UID_MAX` changes future assignments; existing
files remain owned by the old UIDs and should be re-chowned or discarded.

### Directory permissions

Runbox creates `workspaces/` as `0711` and `state/` as `0700` on startup, and
re-applies both on every restart. Do not tighten `workspaces/` to `0700`: the
sandbox UID needs the traverse bit to reach its own working directory, and
every `exec` would fail with `fork/exec /bin/sh: permission denied`. `0711`
allows traversal without listing, which is the property you want.

## Backup

The workspace is a cache. Back it up only if intermediate state matters.
`state/runbox.db` is not a cache: it holds the identity-to-UID mapping. Back it
up if you want stable UIDs; otherwise a restore re-assigns UIDs and re-chowns
workspaces.

SQLite runs in WAL mode. For a consistent copy either stop the container or use
SQLite's backup API/`VACUUM INTO`; copying `runbox.db` while WAL is active can
miss recent commits. Do not put `state/` on NFS or SMB.

Delivered files are not stored twice. The application should download what it
needs and keep that copy; treat Runbox as ephemeral.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `401 unauthorized` | Bearer token mismatch. Check `RUNBOX_API_KEY` on both sides. |
| `400 missing identity` | Neither identity form was sent, or only half of namespace+principal. |
| `400 invalid identity` | Token is not 1-256 visible ASCII, or namespace/principal is outside `[A-Za-z0-9._-]{1,128}`. |
| `400 ambiguous identity` | Both `X-Runbox-Identity` and namespace+principal were sent. |
| `409 busy` | Concurrency limit reached; add replicas or raise `RUNBOX_MAX_CONCURRENCY`. |
| `409 quota_exceeded` on exec | Workspace above quota; delete files or raise `RUNBOX_QUOTA_BYTES`. |
| `413 too_large` | Request/upload over limit; raise `RUNBOX_MAX_REQUEST_BYTES`/`RUNBOX_MAX_UPLOAD_BYTES` and the proxy body size. |
| Timeout despite a large `timeout_seconds` | Value is clamped to `RUNBOX_MAX_TIMEOUT`; the proxy read timeout may also be too low. |
| `409 timeout` but the command keeps running | `CAP_KILL` is missing from the container; add `cap_add: [KILL]`. |
| Zombie/defunct processes accumulate | PID 1 is not reaping; the provided images run tini. If you override the entrypoint, add `--init` or another reaper. |
| No files returned | Write under `out/`, make them non-zero, and create them during the call. Symlinks are skipped. |
| `python: command not found` | You built `minimal` or replaced the image; check `/v1/capabilities`. |
| `operation not permitted` when setting UID | Rootless Docker/Podman may not map the UID pool. Run a rootful runtime, choose a mapped range, or use gVisor/one container per tenant. |
| Startup exits with `missing required Linux capabilities` | Add `CHOWN`, `FOWNER`, `SETUID`, `SETGID`, `DAC_OVERRIDE` and `KILL`; only for local development set `RUNBOX_ALLOW_MISSING_CAPS=1`. |
| `/ready` returns 503 | The data root or state directory is missing or not writable. Check the `/workspace` volume and its permissions. |
| `/metrics` returns 401 | Send `Authorization: Bearer <RUNBOX_API_KEY>`; the endpoint is authenticated. |
| Permission errors in commands | On non-Linux or non-root Linux, UID isolation is disabled and files are owned by the current user. In the container the runner must be root. |
| LibreOffice conversion fails under gVisor | Some syscalls are emulated slowly or missing; test your workload or keep LibreOffice outside the sandbox. |
