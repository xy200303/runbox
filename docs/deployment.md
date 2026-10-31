# Deployment

## Image variants

| Target | Contents | Rough size | Use |
|---|---|---|---|
| `full` (default) | Python, Node, poppler, git, LibreOffice, CJK fonts, data libraries | ~500 MB | Document and spreadsheet work |
| `minimal` | Python, Node, poppler, git; no LibreOffice, no CJK fonts, no Python data libraries | ~150 MB | Code/text workloads |

```bash
docker build -t runbox:latest .
docker build --target minimal -t runbox:minimal .
```

Prebuilt multi-arch images are published by the `docker` workflow:

```text
ghcr.io/xy200303/runbox:latest
ghcr.io/xy200303/runbox:latest-minimal
ghcr.io/xy200303/runbox:v0.2.0
ghcr.io/xy200303/runbox:v0.2.0-minimal
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
- Add a `NetworkPolicy` for egress if commands must not reach the internet.
- Size ephemeral storage with a real quota; `RUNBOX_QUOTA_BYTES` is a soft
  pre-check, not a filesystem limit.

## Sizing

| Resource | Guidance |
|---|---|
| CPU | Each execution is one shell plus its children. LibreOffice and pandas are the heavy paths. `2` CPUs with concurrency `2` is a reasonable start. |
| Memory | Container limit `2 GiB` handles two small Python jobs or one LibreOffice conversion with headroom. Raise it before raising concurrency. |
| Disk | Image ~500 MB (full). Workspace volume: `quota * active identities`. |
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
