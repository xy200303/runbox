# Architecture

## Goals

- Give an agent a Linux workspace it can edit freely, across multiple calls.
- Be the sandbox you run on one server: no cluster or scheduler required.
- Keep execution isolated enough for mutually non-hostile users on one host.
- Keep the deployable unit small: one binary, one container, embedded SQLite.
- Keep the wire contract usable from any language.

## Non-goals

- Strong isolation between hostile tenants. That requires a stronger boundary
  (gVisor, microVM) or one container per tenant.
- Distributed scheduling. One runner owns its local workspaces.
- Long-term storage. `keep/` is a convenient archive, not a backup system.
- Hosting the agent or running the model loop. Runbox executes the commands and
  stores the files the agent asks for; the agent itself runs elsewhere.

## Design rationale: one box, many identities

A container per user is the simplest isolation story, but it duplicates the
runtime on every identity and needs orchestration on a single machine. Runbox
instead keeps one long-lived container and multiplies identities inside it: a
stable UID and a private workspace provide the boundary, and one process serves
many identities.

This is the dify-sandbox shape. Runbox adds a file-isolation layer on top:
per-identity workspace and quota, an immutable archive outside the writable
tree, symlink-safe path resolution and bounded output.

The choice optimizes single-machine deployment and resource use, not
hostile-tenant isolation. The shared kernel is the trade-off; gVisor or one
container per trust domain is the upgrade path, and neither changes the HTTP
contract.

## Components

```
┌────────────┐    HTTP     ┌──────────────────────────────┐
│ SDK/client │ ──────────► │ cmd/runboxd                  │
└────────────┘             │  internal/server (HTTP)      │
                           │  internal/config (env)       │
                           │  internal/runner (executor)  │
                           └──────────────┬───────────────┘
                                          │ fork/exec, setuid
                                          ▼
                                   /bin/sh -c "…"
```

| Package | Role |
|---|---|
| `sdk/go/` | Public Go SDK: wire types, file-id codec, validation, HTTP client. No `internal/` imports. |
| `internal/config/` | Environment parsing and validation. |
| `internal/runner/` | Workspaces, UID allocation, execution, output collection, archive, janitor. |
| `internal/store/` | SQLite identities, UID assignments and metadata (GORM). |
| `internal/server/` | Gin routing, auth, identity, body limits, streaming. |
| `cmd/runboxd/` | Wiring, logging, signals, graceful shutdown. |
| `sdk/python/`, `sdk/node/` | Python and Node clients. |
| `website/` | Static product site deployed to GitHub Pages. |

The server depends on a concrete `*runner.Runner`; a compile-time assertion
keeps it satisfying `sandbox.Backend`. The SDK does not depend on the server.

## Filesystem layout

```
<root>/
├── workspaces/
│   └── <key>/                          0750, owned by the identity UID
│       ├── files/                      uploads
│       ├── out/                        artifacts returned to the caller
│       ├── .tmp/                       TMPDIR, TEMP, TMP
│       ├── .cache/  .config/           XDG directories
│       ├── .local/                     pip --user, XDG data
│       ├── .npm-global/                npm global prefix
│       └── .npm-cache/
├── keep/
│   └── <key>/                          0750, owned by the runner (root)
│       └── <same relative path as out/file>
└── state/
    ├── runbox.db                     SQLite: identities, UIDs, metadata
    ├── runbox.db-wal                 WAL journal while the runner is active
    └── runbox.db-shm                 shared-memory index while active
```

`<root>` is `/workspace` in the image and `.runbox` by default.

Why `keep/` is outside the workspace: a user-owned directory can be renamed,
removed or replaced with a symlink by the command itself. Delivered files must
survive the agent, so they live in a runner-owned tree with mode `0640`.

## Identity and UID model

- Identity is exactly one of two forms:
  - **Token**: a single opaque identifier (1-256 visible ASCII characters);
  - **Scoped**: namespace + principal (each 1-128 `[A-Za-z0-9._-]`).
  Supplying both forms, or half of the scoped form, is rejected.
- Both forms normalize to a SHA-256 canonical key. The key, not the raw value,
  is used for directories, UID assignments, locks and file IDs. Scoped
  namespace/principal are stored in the `identities` table of
  `state/runbox.db` for future tenant-level operations; tokens are never
  persisted.
- On Linux, if the process is root, each identity receives a UID from
  `[RUNBOX_UID_MIN, RUNBOX_UID_MAX)`. The assignment is written to SQLite, so
  the same identity keeps the same UID across restarts and file ownership
  stays valid.
- The pool is finite. If it is exhausted, the request fails with
  `409 uid_exhausted`; UIDs are never derived or reused implicitly, because
  that would conflict with files already owned by another identity.
- Workspace directories and uploads are `chown`ed to that UID. Commands run
  with `syscall.Credential`, clearing supplementary groups.
- If a workspace's `files/` or `out/` directory has a different owner (for
  example after moving data in), the whole tree is `chown`ed before the next
  command. New directories are always created as the runner and handed to the
  identity, so the check stays cheap.
- If the process is not root (local development on Linux) or the platform is
  not Linux (tests), commands run as the current user and UID isolation is
  disabled. The HTTP and file behavior is otherwise identical.

## Concurrency

Two independent mechanisms:

1. **Per-identity mutex.** Serializes all calls for one identity (`SaveFile`,
   `Exec`, `Archive`, `DeleteWorkspace`). This prevents concurrent `chown`,
   workspace deletion and archive races.
2. **Global semaphore.** At most `RUNBOX_MAX_CONCURRENCY` commands at once.
   Acquisition is non-blocking; when full, the request returns `409 busy`
   immediately. A bounded queue was rejected on purpose: queued requests hide
   overload and consume file descriptors.

Reads (`OpenFile`, `OpenPath`, `List`) do not take the lock and can run while a
command is executing.

The janitor uses `TryLock`; a workspace with an execution in flight is skipped.

## Execution lifecycle

1. Validate identity and command, decode every input ID and confirm ownership.
2. Acquire the identity lock and a concurrency slot.
3. Create or repair the workspace and its XDG/home directories.
4. Measure the workspace; refuse when above `RUNBOX_QUOTA_BYTES`.
5. If privileges allow, check the owner of the workspace, `files/` and `out/`;
   a mismatch triggers a one-off recursive chown to the identity's UID.
6. Clamp the timeout and start `/bin/sh -c <command>` with:
   - `Dir` = workspace;
   - a minimal environment built from scratch (`HOME`, `PATH`, `TMPDIR`,
     `XDG_*`, pip/npm prefixes, `LANG`);
   - a new process group (`Setpgid`), and the identity's credentials on Linux.
7. Capture stdout and stderr into bounded head+tail buffers.
8. On timeout or client cancellation, kill the process group (`SIGKILL`), wait
   briefly for the pipes to close, and report `409 timeout` / `408 canceled`.
   This requires `CAP_KILL`; without it the error message says the kill failed.
9. Collect regular files under `out/` modified since the call started, honoring
   count and size caps and skipping symlinks.
10. Release the slot and lock, and return `exit_code`, output and file IDs.

Non-zero exits are normal results, not transport errors.

## Path handling

All paths go through `sandbox.NormalizeRel`:

- backslashes become `/`;
- `./` prefixes are removed;
- absolute paths, drive letters, control characters, empty segments and `..`
  are rejected.

`internal/runner.secureJoin` then:

1. joins lexically and checks containment;
2. resolves symlinks for both the root and the target;
3. checks containment again.

That second check is the important one: a command can create
`out/link -> /etc/shadow`, and the runner runs as root. Reading through the
link would leak a file the identity cannot read by itself.

`secureMkdirAll` creates archive directories one segment at a time and refuses
to traverse a symlink. `copyInto` unlinks an existing destination, then creates
the file with `O_EXCL`, so a planted symlink is never followed.

## Output collection

- Recursively walks `out/`.
- Skips directories, symlinks, zero-byte files and files older than
  `startedAt - 1s` (filesystem timestamp granularity).
- Returns workspace-relative names with `/` separators and opaque file IDs.
- Stops collecting after `RUNBOX_MAX_OUTPUT_FILES` or when the cumulative size
  would exceed `RUNBOX_MAX_OUTPUT_BYTES`.

Ordering is deterministic (sorted by name).

## Archive

`Archive` copies a `files/` or `out/` entry into `keep/` preserving the
relative path. The destination is created under the runner's UID with mode
`0640`; the sandbox identity can neither read nor modify it after delivery.
The archive quota is checked before copying. Archiving an already archived ID
is idempotent.

Archived files never expire; deleting them requires
`DELETE /v1/workspace?purge=true` or removing the volume.

## Retention

A background sweep runs every `RUNBOX_SWEEP_INTERVAL`. For each workspace it:

- computes the newest modification time in the tree;
- if older than `RUNBOX_RETENTION` and no execution holds the identity lock,
  removes the workspace directory;
- never touches `keep/`.

The next call recreates the workspace and its conventional directories.

## Failure behavior

| Failure | Behavior |
|---|---|
| Runner restarts | Workspaces, archives and UID mappings are on disk and survive. If a mapping is lost or the UID range changes, the next command recursively `chown`s the workspace to the new UID. |
| Command killed by signal | `exit_code` is `-1` on Unix; stdout/stderr are still returned. |
| Output pipe held by a grandchild | After a kill, the runner waits at most 5 seconds, then returns instead of leaking the request goroutine. |
| Disk full | Upload/exec returns an internal error; the workspace remains. |
| Corrupt file ID | `400 bad_request`. |
| `keep/` missing | `404 not_found`. |

## Multiple replicas

Workspaces are local. Running several replicas behind a load balancer needs
one of:

- sticky routing by canonical identity key;
- a shared volume with a single writer per identity (not provided);
- accepting that an identity may land on a fresh workspace.

`keep/` is similarly local; a real archive should be copied into object storage
by the application.

## Extension points

- Swap the executor by implementing `sandbox.Backend`.
- Add authentication (JWT, mTLS) in `internal/server` before the identity
  middleware.
- Add a metrics endpoint without changing the runner.
- Add a second storage backend for `keep/` by replacing `Runner.Archive` and
  `Runner.OpenFile`.
