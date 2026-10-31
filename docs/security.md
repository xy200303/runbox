# Security model

Runbox runs arbitrary code on purpose. This document states what it protects,
how, and where the boundary ends.

## Threat model

**Untrusted:** the command string and every file the agent uploads or creates.
It may try to read other users' files, escape the workspace, exhaust resources,
steal the API key or attack the host.

**Trusted:** the runner binary, its configuration, the image and the host.

**Semi-trusted:** the caller holding the API key. The key is a single
deployment-wide secret, not a per-tenant credential. Anyone with it can use any
identity and consume the runner's capacity. Put Runbox behind
an authenticating gateway if callers are not all trusted.

**Assets:** other identities' workspaces, the API key, the host
and other containers.

## Trust boundaries

```
untrusted command ── UID + workspace permissions ──► other identities' files
                  ── container namespaces/cgroups ──► host
```

The container itself is the outer boundary. Runbox hardens the inside; it does
not replace the container runtime.

## Controls

| Control | Implementation | Why |
|---|---|---|
| Per-identity workspace | `<root>/workspaces/<key>` | Prevents one caller from listing another's files by default. |
| Per-identity UID | Stable UID from `[UID_MIN, UID_MAX)`, `syscall.Credential` | A command cannot `chmod` or overwrite another identity's files even if it guesses paths. Supplementary groups are cleared. |
| Persistent UID mapping | `state/runbox.db` (`identities.uid`) | A restart must not hand an identity a new UID while its files keep the old owner. |
| Identity metadata | `state/runbox.db` (`identities`) | Stores mode and scoped namespace/principal for tenant operations; never stores tokens. |
| Symlink-safe workspace dirs | `ensureSubdir` removes and recreates links or non-directories | The workspace is writable by its identity; `mkdir`/`chmod` as root would otherwise follow a planted `files/` link. |
| Directory modes | workspace `0750`, home dirs `0700`, files `0640` | Other UIDs in the container cannot traverse. |
| Minimal environment | `baseEnv` allowlist + explicit overrides | The runner's environment contains `RUNBOX_API_KEY`; commands must not inherit it. |
| Process-group kill | `Setpgid` + `kill(-pid, SIGKILL)` | A timeout kills the shell and its children, not other identities' processes. |
| Kill capability | `CAP_KILL` in the container | A root runner cannot signal a command running as UID 20000 without it. Without `CAP_KILL`, a timeout returns but leaves the command alive. |
| Child reaping | tini as PID 1 | Killed command trees are reparented to PID 1; an app that does not reap accumulates zombies. |
| Bounded output | Head+tail buffer per stream | A `yes` loop cannot exhaust RAM. |
| Output caps | Count and total bytes | Bounds response size and disk reads. |
| Path validation | `NormalizeRel` + `secureJoin` + symlink resolution | Blocks `..`, absolute paths, drive letters and symlink escapes. |
| Constant-time key check | `subtle.ConstantTimeCompare` | Avoids trivial timing oracles. |
| Strict identity validation | Token: 1-256 visible ASCII; scoped parts: `[A-Za-z0-9._-]{1,128}` | Sanitizing would merge distinct identities. Supplying both modes is rejected. |
| Upload size enforcement | Limit reader, not `Content-Length` | A lying header cannot fill the disk. |
| Request body cap | `http.MaxBytesReader` | Bounds multipart parsing memory/disk. |
| Graceful shutdown | `http.Server.Shutdown` | In-flight commands finish or are cut by timeout. |
| Health without auth | `/health` and `/ready` only | Probes work; no data is exposed. `/ready` checks storage writability. |
| Metrics authentication | `/metrics` requires `Authorization: Bearer` | Counters are operational data, not public. Labels are bounded. |

## Container hardening

The provided compose file:

- runs the root filesystem read-only;
- mounts an executable tmpfs at `/tmp` (LibreOffice and package managers need
  it) and nothing else executable;
- drops all capabilities, adding back only `CHOWN`, `FOWNER`, `SETUID`,
  `SETGID` and `DAC_OVERRIDE` for workspace ownership, plus `KILL` so a timeout
  can signal a command running as the identity's UID;
- runs a tiny init (tini) as PID 1 so killed command trees are reaped instead
  of accumulating as zombies;
- fails closed at startup when the process is root and a required capability is
  missing; `RUNBOX_ALLOW_MISSING_CAPS=1` exists only for local development;
- sets `no-new-privileges`;
- limits memory, CPU and pids;
- mounts only the workspace volume;
- declares a health check and `restart: unless-stopped`.

The container intentionally runs as root **inside its own namespace**. Without
that it cannot assign UIDs. This is not host root; Docker's default namespace,
capability and seccomp configuration still apply.

## What Runbox does not protect against

### Shared kernel

Containers share the host kernel. A kernel or runtime vulnerability can turn a
container escape into host compromise. Keep the host and runtime patched. For
hostile tenants, use gVisor or a microVM.

### Cross-identity process visibility

All identities share the container's PID namespace. A command can see other
processes (PID, command line) through `/proc`, even though it cannot signal
them. Process arguments could contain sensitive fragments. Mitigations:
gVisor, one container per tenant, or a custom `hidepid=2` `/proc` mount (not
provided).

### Shared `/tmp`

`TMPDIR`/`TEMP`/`TMP` point inside the workspace, but some tools hardcode
`/tmp`, which is the container tmpfs shared by all identities. Files created
with mode `0600` are not readable by other UIDs, but filenames are visible.
For strict separation, give each tenant its own container.

### Resource fairness

Timeouts are per command; memory, CPU and pids are per container. One identity
can consume most of the container's memory or CPU and slow others. There are
no per-identity cgroups. If fairness matters, cap concurrency at 1 or split
tenants across containers.

### Disk quota is not a hard limit

`RUNBOX_QUOTA_BYTES` is checked **before** a command starts. A single command
can still write past it until the volume is full. Enforce a real volume quota
(XFS project quota, Kubernetes `ephemeral-storage`, or a separate volume per
tenant) when disk exhaustion is a concern.

### Network egress

Commands can reach anything the container can reach. There is no built-in
allowlist. Use a network policy, firewall or an egress proxy; for an air-gapped
runner, remove the network entirely after pulling the image.

### Shared API key

One key authorizes every identity and has no rate limit. Anyone holding it can
exhaust concurrency. Use a gateway with per-caller auth and quotas, or run one
runner per trust domain.

### Lingering processes and root-side races

A command can leave a background process running (`nohup … &`). It keeps
running as the identity's UID after the call returns. Runbox serializes its own
mutations per identity, but does not kill leftover processes at command end
(only the timed-out process group is killed). A lingering process can race
root-side file operations — for example, swapping `files/` for a symlink while
the runner prepares the workspace. UID permissions still stop it from reading
another identity's files, and path resolution re-checks symlinks, but a hostile
lingering process is best handled by a stronger boundary (gVisor, one container
per tenant) or by rebuilding the container between sessions. Do not treat the
workspace as a trusted directory while a command may still be alive.

### No custom seccomp profile

The image relies on Docker's default seccomp profile. A tighter profile is
possible but not supplied; gVisor is the recommended stronger boundary.

## Deployment checklist

- [ ] Generate a long random `RUNBOX_API_KEY` (`openssl rand -hex 32`); never
      commit it.
- [ ] Bind the port to a private network or put TLS and authentication in front.
- [ ] Keep `read_only`, `cap_drop`, `no-new-privileges`, and the tmpfs mount.
- [ ] Set memory, CPU and pid limits appropriate to your workload.
- [ ] Add a network policy or firewall for egress.
- [ ] Enforce a real disk quota on the workspace volume.
- [ ] Rebuild/recreate the container daily to discard runtime state
      (`docker compose up -d --force-recreate`); the workspace volume survives.
- [ ] Monitor `/health`, disk usage, `busy` and `quota_exceeded` rates.
- [ ] Patch the host, Docker and the image regularly.
- [ ] Do not bake application secrets into the image; commands can read any
      world-readable file in it.

## Why not one container per user

A container per user gives a stronger boundary, but on a single machine it
duplicates the runtime and needs a scheduler or a pool. Runbox chooses one
container with per-identity UIDs and workspaces, plus the file-isolation layer
described above. That is the right default for semi-trusted users and internal
agents; it is not a claim that a shared kernel is a microVM boundary. The
sections below are the upgrade path when the trust level changes.

## Stronger isolation

### gVisor (smallest change)

Install `runsc` on the host, then:

```bash
docker run --runtime=runsc ... runbox:latest
```

or in compose:

```yaml
services:
  runbox:
    runtime: runsc
```

The application and its filesystem layout do not change. Syscall-heavy
workloads (some Python/NumPy paths) may be slower; measure before switching.

### microVM

E2B, microsandbox and similar projects run each sandbox in a microVM
(Firecracker/libkrun). Runbox's HTTP contract could be implemented behind such
a runtime, but this repository does not do so. Use them when tenants are
mutually hostile or the host runs untrusted code from the internet.

### One container per tenant

For a small number of strong tenants, give each its own Runbox container and
volume. Isolation becomes the container boundary, and a noisy tenant cannot
see or starve another. The cost is operational: one service and volume each.

## CVEs and patching

Container security is four layers. Patching one layer does not fix the others.

| Layer | Who owns it | How it gets fixed |
|---|---|---|
| Host kernel | The machine operator | Kernel updates, reboot/livepatch, hypervisor patching. Containers share the kernel; an image update cannot fix a kernel CVE. |
| Container runtime | The machine operator | Update Docker/containerd/runc, or the Kubernetes node runtime. Runtime escapes are usually host-side bugs. |
| Image packages | Runbox image maintainers | Rebuild from the current `debian:bookworm-slim` and Debian security updates; do not run a months-old image. |
| Go/npm/Python dependencies | Upstream projects | `govulncheck`, `npm audit`, Dependabot, and regular upgrades. |

`debian:bookworm-slim` is a moving tag: a rebuild picks up Debian point releases
and security fixes. The CI build uses the current base; production should rebuild
on a schedule (for example weekly) and after Debian security advisories, not only
when application code changes.

Runbox does not claim a zero-CVE image. No image can. The workflow is:

1. `security.yml` runs `govulncheck`, `npm audit`, and a Trivy image scan for
   critical fixable issues on pushes and weekly.
2. Dependabot proposes Go, npm, Python and GitHub Actions updates.
3. Image scans report the package and CVE; triage `ignore-unfixed` findings
   separately. Rebuild after the base or a dependency is fixed.
4. `docker.yml` publishes provenance and SBOM attestations with tagged images.
5. The host operator patches the kernel and container runtime; for hostile
   tenants, run under gVisor or a microVM, which also narrows the host kernel
   surface.

The `full` image includes LibreOffice, a large parser with a correspondingly
large CVE surface. For untrusted documents, prefer the `minimal` image plus a
dedicated converter service, or run the full image under gVisor. Keep the
read-only root, dropped capabilities, `no-new-privileges`, seccomp defaults and
egress policy in place; they reduce the impact of a userspace CVE but do not
replace patching.

## Reporting

See [SECURITY.md](../SECURITY.md). Do not open a public issue for a
vulnerability.
