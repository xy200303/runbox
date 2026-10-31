# Validation

Three standard-library scripts that attack a live runner the way an untrusted
caller would. They are the source of the numbers on
[runbox.xyun.dev/validation](https://runbox.xyun.dev/validation).

| Script | What it does | Checks |
| --- | --- | --- |
| `rbtest.py security` | Auth, identity headers, cross-tenant reads, traversal and encoding, container escapes, resource limits | 78 |
| `deep.py` | Hardlinks, FIFOs, symlink races, `/proc` visibility, kernel escapes, request smuggling, archive limits, UID recycling | 36 |
| `obfuscation.py` | Base64/hex/eval payloads, nested scripts, path splicing, dynamic `unshare`/`chroot`, setuid copies, repeated attempts | 26 |
| `rbtest.py perf` | exec latency, concurrency throughput, upload/download, Range reads, many files | 16 |

## Running against a local instance

```bash
docker run -d --name rb -p 8099:8099 \
  -e RUNBOX_API_KEY=test-key-123 -e RUNBOX_UID_MAX=40000 \
  -v rb-data:/workspace ghcr.io/xy200303/runbox:latest

RB_PORT=8099 RB_KEY=test-key-123 python validation/rbtest.py security
RB_PORT=8099 RB_CONTAINER=rb     python validation/deep.py
RB_PORT=8099                     python validation/obfuscation.py
RB_PORT=8099                     python validation/rbtest.py perf
```

`deep.py` shells out to `docker exec <container> cat /etc/hostname` for the
symlink-race assertion; point `RB_CONTAINER` at the container name you started.

## Reading the output

Every check prints `PASS`/`FAIL` with the observed status or response fragment,
and the script exits non-zero if anything failed. The suite is written for a
disposable instance: it creates hundreds of identities, writes megabytes into
workspaces and intentionally trips quota and concurrency limits.

## What the suites do not cover

Kernel exploits, gVisor or microVM isolation, hard filesystem quotas, egress
filtering and multi-replica routing. Those need a different environment; the
[validation page](https://runbox.xyun.dev/validation) lists the mitigated
residual risks.
