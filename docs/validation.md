# Validation

Runbox isolates with the kernel, not with string matching: the runner switches
to the identity's UID before `fork/exec` and resolves every path inside that
identity's workspace. How a command is spelled, encoded or nested does not
change that. This page records the numbers behind the claim, and the boundaries
that remain.

The same results, with charts, are published at
[runbox.xyun.dev/validation](https://runbox.xyun.dev/validation). Raw harnesses
live in [`validation/`](../validation/).

## Environment

| Item | Value |
| --- | --- |
| Image | built from this repository's current commit |
| Runtime | Docker Desktop, Linux container, 16 vCPU / 24 GiB |
| Defaults | concurrency 2, quota 1 GiB, upload 20 MB, stdout 200 KiB, attachments 64 |
| Identity | a fresh identity per test, so cached state cannot hide a failure |

```bash
RB_PORT=8099 RB_KEY=... python validation/rbtest.py security
RB_PORT=8099 RB_CONTAINER=rb python validation/deep.py
RB_PORT=8099 python validation/obfuscation.py
RB_PORT=8099 python validation/rbtest.py perf
```

## Coverage

| Category | Checks | Result |
| --- | --- | --- |
| Paths and encoding | 22 | all rejected, nothing leaked |
| Identity headers | 19 | 400 on missing, mixed, half, invalid, duplicate or over-long |
| Container boundary | 11 | non-root, read-only root, no `sudo`, no cross-UID signals, no namespaces |
| Authentication | 10 | 401 without a valid bearer; only `/health` and `/ready` anonymous |
| Identity isolation | 9 | cross-tenant reads return `404` or `Permission denied` |
| Resource limits | 4 | 413/400/409 at the documented limits |
| HTTP surface | 3 | wrong methods rejected |
| Deep attacks | 36 | see below |
| Obfuscation and nesting | 26 | see below |

Total: 140 checks, 0 bypasses.

## Deep attacks

| Attack | Technique | Result |
| --- | --- | --- |
| Hardlink smuggling | `ln /etc/shadow out/x` then download | kernel `protected_hardlinks` refuses |
| Special file blocking | `mkfifo out/pipe` then request it | `Lstat` first; 404 in ~2 ms, request never blocks |
| Symlink race | 3 readers against 1 swapper for 4 s | 0 successful reads, 0 escapes |
| Archive race | same swap through `/v1/archive` | 0 escapes |
| Process environment | read `/proc/1/environ` | Permission denied |
| Process command line | read another identity's `/proc/<pid>/cmdline` | **visible**; documented boundary |
| Kernel escapes | `unshare -Urm`, `mount`, `chroot` | EPERM |
| Runtime back doors | `/var/run/docker.sock`, `/dev/mem` | absent |
| Request smuggling | two `command` fields in one multipart body | 400; the second never runs |
| Parameter pollution | `?path=a&path=../../etc/shadow` | first value wins, no escape |
| Archive amplification | 1001 `path` parameters | 400 |
| UID recycling | delete a workspace, create the next identity | starts empty, cannot read the old path |

## Obfuscation and nested scripts

| Technique | Payload | Result |
| --- | --- | --- |
| Base64 | `echo … \| base64 -d \| sh` | `/etc/shadow` permission denied |
| Hex + eval | `printf … \| xxd -r -p \| sh` | denied (the image has no `xxd`; shell error) |
| Three-level scripts | a writes b, b writes c, c carries the payload | cannot read the state database or another workspace |
| Script on disk | write `out/x.sh`, run it with `sh`/`bash` | boundary unchanged |
| Path splicing | variables, `printf`, brace expansion, quotes, command substitution, here-doc | 8 spellings, none reads `/etc/shadow` |
| Assembled escapes | `unshare`, `chroot` built from variables | still EPERM |
| setuid copy | `cp /bin/sh`, `chmod 4755`, run it | effective UID stays the sandbox UID |
| Repetition | 40 shadow reads, 20 symlink reads | all denied |

## Performance

Measured on a single container with the default concurrency of 2. Numbers come
from Docker Desktop on a 16 vCPU host; a real deployment is bounded by its own
CPU and disk.

| Metric | Result |
| --- | --- |
| `exec true` | p50 8.5 ms, p95 30.9 ms, max 33 ms |
| New identity, first exec (UID + workspace) | p50 20.2 ms |
| `python3 -c pass` | p50 38.2 ms |
| `node -e 0` | p50 196.8 ms |
| 2 / 4 / 8 / 16 identities in parallel | 67 / 367 / 493 / 667 exec/s |
| Same identity, 3 × `sleep 1` | 3.03 s (strictly serialized) |
| Over concurrency (8 × `sleep 2`, limit 2) | 6 fail fast with 409, 2 run |
| Upload 1 / 10 / 19 MB | 29.9 / 40.8 / 60.4 MiB/s |
| Download 32 MB | 66.4 MiB/s |
| Range read 1 MB of 32 MB | 46 ms, `206` with `Content-Range` |
| 500 files | list 10 ms, tar 60 ms |
| One exec with 20 attachments | 30 ms |

## Known boundaries

| Boundary | Effect | Mitigation |
| --- | --- | --- |
| Cross-identity command line | `/proc/<pid>/cmdline` is world-readable; `ps` shows other tenants' arguments | keep secrets out of argv; gVisor, one container per tenant, or `hidepid=2` |
| Shared `/tmp` | writable by every identity | do not stage secrets there; rebuild containers regularly |
| Background processes | a `setsid` daemon outlives its request | deleting or expiring the workspace reaps it |
| Soft quota | one command can overshoot the quota | free space with `DELETE /v1/path`, or enforce hard quotas on the host |
| No custom seccomp | Docker's default profile applies | use gVisor when you need a tighter boundary |
| Shared kernel | container-level isolation, not a microVM | run the whole container under gVisor or a microVM for hostile tenants |
