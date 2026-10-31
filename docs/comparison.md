# Comparison with other sandbox approaches

This is a decision aid, not a benchmark. Product capabilities and licenses
change; verify the current upstream documentation before committing. The table
reflects the state around 2026-10 and groups projects by the problem they
solve.

## The core choice: one box, many identities

A container-per-sandbox design gives every sandbox its own container; a microVM
design gives it its own kernel. Both are strong, and both are heavy on a single
machine: one more container or VM per user, another copy of the runtime, another
thing to schedule.

Runbox chooses the dify-sandbox shape: one long-lived container, many
identities. A stable UID and a private workspace provide the isolation; the
workspace is `0750` and owned by that UID. No container or VM is created per user. The trade-off is a shared
kernel; use gVisor or one container per trust domain when that matters.

Runbox adds the file isolation layer to that shape: per-identity workspace and
quota, symlink-safe path resolution and bounded output. Persistence is left to
the caller. This
is the difference from a plain per-task UID sandbox.

## What matters for an agent sandbox

1. **Isolation boundary** — shared kernel, user-space kernel, microVM or a
   separate host. This decides how much you trust a hostile command.
2. **Lifecycle** — one-shot command, persistent workspace, or long-lived
   environment with snapshots.
3. **Deployment weight** — a container next to your app, a runtime on every
   node, a control plane, or a managed service.
4. **File transfer** — how inputs arrive and artifacts leave.
5. **Language and protocol** — HTTP/OpenAPI, gRPC, language SDKs.
6. **Operations** — quotas, concurrency, cleanup, observability, upgrades.

## Options at a glance

| Option | Isolation boundary | Container/VM per sandbox? | What you operate | File isolation | Good fit |
|---|---|---|---|---|---|
| **Runbox** | container namespace/cgroup + per-UID files | no | one Go binary in one container | built-in per identity | single machine, semi-trusted users |
| **gVisor** (`runsc`) | user-space kernel | no (isolation layer) | Runbox or any container plus the runtime | unchanged | stronger isolation with minimal change |
| **E2B (self-hosted)** | Firecracker microVM | yes | KVM hosts, orchestrator, SDK | strong | many mutually hostile tenants |
| **microsandbox** | microVM (libkrun) | yes | a host runtime plus SDK | strong | fast microVMs without a control plane |
| **OpenSandbox / CubeSandbox** | container pool behind a control plane | typically yes | scheduler, pool manager, API | depends on volume/API | cluster-scale allocation |
| **Daytona / Modal** | managed environments/containers | varies | external platform or paid service | varies | teams preferring managed services |
| **Dify sandbox** | container + per-task UID | no | the Dify stack | basic/task scoped | you already run Dify |
| **Kubernetes Agent Sandbox style CRDs** | Pod/VM per sandbox via a controller | yes | Kubernetes and a controller | strong | teams standardized on Kubernetes |

OpenSandbox and CubeSandbox are control-plane projects and support different
deployment modes; "typically yes" describes the common container-per-sandbox
model, not every mode. Check the current upstream documentation.

## Runbox versus the common alternatives

### Runbox

Choose it when:

- you want one container next to the application, not a new platform;
- you do not want a container or VM per user on a single machine;
- commands are semi-trusted (your own users, internal tools, a SaaS tenant
  with a contract), not anonymous internet traffic;
- the workload is Python/Node/shell, spreadsheets, PDFs and data files;
- you need an execution and file backend for an agent, not an agent runtime or
  model host;
- you need per-identity file isolation: private workspace, quota, symlink-safe
  paths, with persistence handled by the caller;
- you want to read and modify the whole implementation (a small Go codebase).

Avoid it when:

- tenants are mutually hostile and may exploit kernel bugs;
- you need snapshot/restore, GPU passthrough or per-tenant network namespaces;
- you need hundreds of sandboxes per node with fast cold starts.

### gVisor in front of Runbox

Install `runsc`, add `--runtime=runsc` (compose: `runtime: runsc`). No code or
image change; the syscall boundary becomes a user-space kernel. This is the
best effort/benefit upgrade for Runbox. Measure CPU-heavy Python/NumPy and
LibreOffice first: syscall interception has a cost, and a few operations may
need workarounds.

### microVM products (E2B, microsandbox)

A microVM per sandbox gives a separate kernel and a much smaller blast radius.
You pay in infrastructure (KVM, VM image build, networking, storage) and in
startup time, unless the product keeps a warm pool. They are the right choice
when commands come from the public internet or from mutually hostile tenants.

Runbox's HTTP contract could be implemented behind a microVM runtime, but this
repository does not do that. Using both is reasonable: Runbox for internal
workloads, microVMs for the risky tier.

### Container pool + control plane (OpenSandbox, CubeSandbox)

These allocate a container per sandbox from a pool, add scheduling across nodes
and expose a central API. That is the right shape when one machine is not enough
or when each sandbox needs its own container boundary. The cost is a control
plane plus many containers to run, patch and secure — the part that is awkward
on a single machine. Runbox keeps one container and isolates identities inside
it; start there when deployment weight matters, and add a pool when capacity or
per-sandbox isolation, not file isolation, is the bottleneck. Check the current
upstream documentation: these projects support more than one deployment mode.

### Dify's sandbox

If you already run Dify, its code node sandbox is convenient and follows a
similar model (one container, per-task UID, HTTP). Runbox borrows that shape and
adds a per-identity file isolation layer: workspace, quota, symlink-safe path
resolution and bounded output.
Dify's sandbox remains the right choice when you are already inside its
workflow; Runbox is standalone and usable from any application.

## Benchmark before deciding

Measure with your real commands, not `echo`:

| Metric | Why |
|---|---|
| Cold start of the image | LibreOffice and Python imports dominate. |
| p50/p95 command latency | Syscall interception shows up here under gVisor. |
| Memory per concurrent command | Sets `RUNBOX_MAX_CONCURRENCY` and the container limit. |
| Upload/download throughput | Multipart and volume speed. |
| Disk growth per identity | Quota and cleanup policy. |
| Escape surface of your command set | Do commands need raw sockets, `/dev`, mounts, nested containers? |

## Recommendation

1. Start with **Runbox** and the provided compose hardening.
2. Add a real volume quota and an egress policy; these are the two easiest
   operational gaps to close.
3. If commands become less trusted, switch the runtime to **gVisor** and
   re-run the benchmark.
4. If tenants become mutually hostile or you need many per node, move to a
   **microVM** backend or one container per trust domain.
