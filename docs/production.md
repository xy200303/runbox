# Production checklist

Runbox is an execution and file backend for agents, not an agent runtime. This
page is the commercial-deployment checklist: topology, capacity, security,
observability, data lifecycle and runbooks. It assumes the standard deployment:
one Runbox container per volume, reached by the application over HTTP.

## Reference topology

```
caller / agent platform
        │  HTTPS + API key (gateway)
        ▼
   Runbox container ──► /workspace volume
        │                     ├── workspaces/<key>/   mutable
        │                     └── state/runbox.db     identities + UIDs
        │
        └── outbound: package mirrors only (or no egress)

platform file layer (object storage / upload_file)
        ▲
        └── the application copies delivered artifacts here, not Runbox
```

Runbox does not authenticate identities. The caller authorizes the request and
passes exactly one identity form: `X-Runbox-Identity` (token), or
`X-Runbox-Namespace` + `X-Runbox-Principal` (scoped). Both normalize to a
canonical key; scoped namespace/principal and UID assignments are stored in the
`identities` table of `state/runbox.db` for tenant-level operations, while
tokens are not persisted. A single bearer token authorizes the whole deployment, so put the
service behind a gateway if callers are not all trusted.

## Hard prerequisites

- [ ] A rootful container runtime. UID isolation needs `setuid` to the identity
      UID; rootless runtimes may not map the pool.
- [ ] `CAP_KILL`, `CHOWN`, `FOWNER`, `SETUID`, `SETGID`, `DAC_OVERRIDE`. Runbox
      fails closed at startup if any is missing when running as root.
      `RUNBOX_ALLOW_MISSING_CAPS=1` is for development only.
- [ ] Read-only root filesystem, executable tmpfs at `/tmp`, a writable
      `/workspace` volume, and `no-new-privileges`.
- [ ] Memory, CPU and pids limits on the container. One identity can consume the
      container's budget; there are no per-identity cgroups.
- [ ] A real volume quota. `RUNBOX_QUOTA_BYTES` is a pre-execution check, not a
      filesystem limit; one command can still fill the volume.
- [ ] An egress policy or proxy. Runbox does not restrict outbound traffic.
- [ ] TLS and caller authentication at the gateway. The in-process listener is
      plain HTTP by default.

## Capacity starting point

Measure with your real commands; these are starting values, not guarantees.

| Resource | Starting point | Notes |
|---|---|---|
| Container CPU | 2 vCPU | LibreOffice and pandas dominate. |
| Container memory | 2 GiB | One large Python job or one office conversion with headroom. |
| `RUNBOX_MAX_CONCURRENCY` | 2 | Excess requests fail fast with `409 busy`; add replicas before raising indefinitely. |
| Workspace quota | 1 GiB per identity | Includes installs and caches. |
| Retention | 7 days idle | `RUNBOX_RETENTION`. |
| Pids limit | 256 | Stops fork bombs while allowing LibreOffice helpers. |
| Image | full or minimal | Minimal is ~585 MB unpacked; full is ~1.4 GB and adds LibreOffice/data libraries. |

Suggested initial SLOs for an internal deployment:

- readiness success ≥ 99.9% over 30 days;
- `5xx` < 0.1% of requests;
- `409 busy` < 1% at normal load;
- timeout rate < 0.5%;
- p95 request latency for non-exec endpoints < 100 ms;
- exec overhead (excluding the command) < 100 ms.

## Security operations

- Generate a long random API key; store it in a secret manager, not in the
  image or the repository. It is a single deployment-wide secret with no
  in-process rotation: rotate by updating the secret and recreating the runner,
  or terminate authentication at the gateway.
- Terminate TLS and authenticate callers at the gateway. If the gateway sets
  identity headers, strip client-supplied copies.
- Keep the capability set minimal; do not add `SYS_ADMIN`, `NET_ADMIN` or
  `DAC_READ_SEARCH`.
- Patch the host, Docker/containerd and the image. Rebuild monthly at minimum.
- Recreate the container daily (`--force-recreate`) to discard runtime state
  installed outside `/workspace`; the volume survives.
- Use gVisor (`--runtime=runsc`) or one container per trust domain when tenants
  are mutually hostile. The shared kernel is the documented boundary.
- Do not bake application secrets into the image; commands can read
  world-readable files.

## Data lifecycle

- The workspace is a cache: inputs, installs, intermediate files, outputs.
- `out/` is returned to the caller and downloadable by path; the caller owns
  persistence.
- `state/runbox.db` holds identities and UID assignments. Back it up with
  SQLite tooling (or stop the runner first); keep it on local block storage, not
  NFS/SMB.
- Deleting the volume deletes workspaces and UID mappings. Runbox does
  not read the old sandbox layout; there is nothing to convert.

## Observability

| Signal | Endpoint | Use |
|---|---|---|
| Liveness | `GET /health` | Process is up; restart policy. |
| Readiness | `GET /ready` | Data dirs exist and state is writable; remove from rotation on 503. |
| Metrics | `GET /metrics` | Prometheus text; auth with `Authorization: Bearer <key>`. |
| Logs | stdout | JSON with `runbox`; access lines carry `request_id`, status, bytes, duration. |

Alert on:

- readiness failures;
- `5xx` rate and `4xx` auth failures;
- `runbox_exec_errors_total{reason="timeout"}` and `{reason="busy"}`;
- disk usage on the workspace volume;
- container restarts and OOM kills;
- startup failure with `missing required Linux capabilities`.

## Runbooks

### Startup exits with missing capabilities

Add `CHOWN`, `FOWNER`, `SETUID`, `SETGID`, `DAC_OVERRIDE`, `KILL` to the
container. Do not use `RUNBOX_ALLOW_MISSING_CAPS=1` in production: without
`KILL`, timeouts return while commands keep running.

### `/ready` returns 503

The data root or state directory is missing or not writable. Check the
`/workspace` volume mount, ownership and disk space. Recreate the container only
after the volume is writable.

### `409 busy`

The runner is at `RUNBOX_MAX_CONCURRENCY`. Add replicas with identity-sticky
routing, or raise concurrency only after measuring memory and CPU. There is no
request queue by design.

### Timeout

A command exceeded `RUNBOX_MAX_TIMEOUT` (or the request timeout). The process
group is killed; the client gets `409 timeout`. Split the command or raise the
limit deliberately. A missing `KILL` capability makes the kill fail, and the
error says so.

### Disk full

`RUNBOX_QUOTA_BYTES` blocks the next execution but does not stop a running
command from filling the volume. Free space, delete workspaces
(`DELETE /v1/workspace`), or enforce a volume quota.

### `/metrics` returns 401

Configure the scraper with `Authorization: Bearer <RUNBOX_API_KEY>`. The
endpoint is deliberately not public.

### Container runtime storage errors

Docker Desktop or containerd storage errors (`metadata.db: input/output error`,
snapshot failures) are host-runtime problems. Restart the runtime, check disk
and snapshot health, then recreate the container. Runbox does not manage the
container runtime.

## Multi-replica

Workspaces are local to one runner. Multiple replicas require one
of:

- sticky routing by canonical identity key to the same volume/runner;
- one volume per replica with identity affinity;
- accepting that an identity may land on a fresh workspace.

Do not mount the same `/workspace` read-write into two runners: UID mappings,
the janitor and SQLite writes assume a single writer.

## Upgrade and rollback

1. Build and tag a new image; record the version exposed by `/health` and
   `runbox_build_info`.
2. Recreate one runner; wait for `/ready`.
3. Run the upload → exec → download → path download smoke test.
4. Roll out the rest.
5. On rollback, switch the image tag back; the volume layout is stable within
   Runbox. There is no compatibility with the old sandbox layout.

`RUNBOX_SHUTDOWN_TIMEOUT` controls how long in-flight requests may finish during
recreate; after that they are cancelled and their process groups are killed.

## Commercial readiness checklist

- [ ] API key in a secret manager; gateway auth and TLS in front.
- [ ] Capability set exactly as required; startup fails closed.
- [ ] Read-only root, tmpfs, volume, memory/CPU/pids limits.
- [ ] Real volume quota and disk alerting.
- [ ] Egress policy.
- [ ] `/ready` in the orchestrator, `/metrics` scraped, logs collected.
- [ ] Alerts for readiness, 5xx, busy, timeout, disk and restarts.
- [ ] Retention policy agreed with the application.
- [ ] gVisor or one container per trust domain for hostile tenants.
- [ ] Backup policy only for data that is the sole copy.
- [ ] Upgrade and rollback rehearsed; smoke test automated.
