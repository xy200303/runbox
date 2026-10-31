# Hard cutover from the old sandbox

This page is for the property-management platform that provided the original
sandbox code. It is a **cutover checklist, not a compatibility guide**.

Runbox does not accept the old headers, environment names, file IDs, workspace
layout or response quirks. The old client cannot be pointed at the new service
and expected to work. A caller integrates by adopting the new API or the Go
client, and the old code path is removed.

## What was extracted (history only)

| File | Lines | Responsibility |
|---|---:|---|
| `backend/internal/sandbox/sandbox.go` | 113 | Contract, limits, `Output`/`Result`, file-id codec, helpers |
| `backend/internal/sandbox/http.go` | 200 | Application-side HTTP client |
| `backend/internal/sandbox/runner/local.go` | 379 | In-container executor |
| `backend/internal/sandbox/runner/local_linux.go` | 36 | Linux credentials and execution |
| `backend/internal/sandbox/runner/local_other.go` | 14 | Non-Linux stub |
| `backend/cmd/sandbox-runner/main.go` | 216 | Old HTTP service |
| **Total** | **1004** | |

The module was already close to standalone: its only application dependency was
`internal/pkg/errs`, and it imported no framework or business package. Runbox
keeps the good parts (one container, per-identity workspace and UID, HTTP file
transfer) and replaces the rest with a single new contract.

## The rule: one contract

Runbox has exactly one API:

| Concern | Accepted by Runbox |
|---|---|
| Identity | `X-Runbox-Identity` or `X-Runbox-Namespace` + `X-Runbox-Principal` |
| Authentication | `Authorization: Bearer` |
| Identity metadata | `state/runbox.db` (`identities`) |
| Configuration | `RUNBOX_*` environment variables only |
| File IDs | `rbx_` prefix, versioned four fields (version, key, kind, path) |
| Workspace | `<root>/workspaces/<key>` |
| Archive | `<root>/keep/<key>` |

The following are **rejected**, not translated:

- `X-Tenant-Id`, `X-User-Id`;
- `SANDBOX_RUNNER_ADDR`, `SANDBOX_RUNNER_KEY`, `AGENT_SANDBOX_ROOT`;
- `sbx_` file IDs, five-field file IDs, in-workspace `keep/` paths;
- old workspace data under `/workspace/users/`.

If a caller wants the old behavior, run the old service next to Runbox and
migrate deliberately. Do not add a compatibility layer to Runbox.

## Cutover steps

### 1. Publish the Go module (or pin it locally)

The module path in `go.mod` is a placeholder. Publish this repository under your
organisation and update the path, or, for a private trial, point the property
backend at the local checkout:

```go
replace github.com/xy200303/runbox => C:/path/to/runbox
```

Do not copy the `sandbox` package into the property repository; the point of the
module is one versioned client used by the caller.

### 2. Replace the application client

Delete or stop importing `backend/internal/sandbox/http.go` and stop running
`backend/cmd/sandbox-runner`. Build a `sandbox.Client` instead:

```go
import (
    "time"

    "github.com/xy200303/runbox/sdk/go"
)

client, err := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL: cfg.AgentSandboxURL,
    APIKey:  cfg.AgentSandboxKey,
})
if err != nil {
    return err
}

// Scoped mode: the application's tenant/user model maps directly.
identity := sandbox.ScopedIdentity(tenantID, userID)
result, err := client.Exec(ctx, identity, command, inputs, 60*time.Second)

// Token mode is equivalent for callers that already have one identifier:
// identity := sandbox.TokenIdentity("01J...")
```

The client is safe to share; `WithIdentity` returns a copy bound to one
identity. The ergonomic form supports attachments:

```go
result, err := client.WithIdentity(identity).Run(ctx, sandbox.ExecRequest{
    Command: "python3 analyze.py files/input.csv",
    Timeout: 60 * time.Second,
    Files:   []sandbox.FileUpload{{Name: "input.csv", Reader: file}},
})
```

Every method is context-aware:

```go
reader, name, err := client.OpenFile(ctx, identity, fileID)
reader, name, err = client.OpenPath(ctx, identity, "out/report.xlsx")
archived, err := client.Archive(ctx, identity, fileID)
entries, err := client.List(ctx, identity, "out")
err = client.DeleteWorkspace(ctx, identity, false) // false keeps archived deliverables
```

The returned `*sandbox.Result` uses `exit_code`, `stdout`, `files[].file_id`
and `files[].size`. Errors are `*sandbox.Error` with `Status`, `Code` and
`Message`; map them into the application's error type at the boundary if the
existing API needs business codes.

### 3. Adapt the AI runtime

If the runtime currently aliases the backend interface:

```go
type Backend = sandbox.Backend
```

then update the call sites:

- `Exec` takes a `time.Duration`, not seconds;
- `SaveFile`, `OpenFile`, `OpenPath`, `Archive`, `List` and `DeleteWorkspace`
  take a `context.Context`;
- `DeleteWorkspace` and `List` are new methods;
- errors are `*sandbox.Error`.

If you must keep the old in-process signature temporarily, write an adapter in
the application; do not put it in Runbox. For example:

```go
type runboxBackend struct{ client *sandbox.Client }

func (b runboxBackend) Exec(ctx context.Context, tenant, user, command string, inputs []string, timeoutSeconds int) (*sandbox.Result, error) {
    identity := identityFor(tenant, user) // the caller's derivation
    return b.client.Exec(ctx, identity, command, inputs,
        time.Duration(timeoutSeconds)*time.Second)
}
```

### 4. Update deployment

- Build the Runbox image and run it as the `sandbox` service.
- Set `RUNBOX_API_KEY`; do not reuse old environment names.
- Add `KILL` to `cap_add`. Without it, the runner cannot signal a command
  running as the identity's UID, so a timeout returns while the command keeps
  running.
- Keep the read-only root, executable tmpfs at `/tmp`, and the `/workspace`
  volume. The image already runs tini as PID 1 to reap killed trees.
- Point the application's URL and key at the new service.

### 5. Decide what happens to old data

Runbox does not read old data:

- old `sbx_` IDs are rejected, including IDs stored in the application
  database;
- old workspaces under `/workspace/users/...` and old in-workspace `keep/`
  directories are ignored;
- new file IDs use `rbx_` and refer only to files created through Runbox.

Before switching, export any old artifacts that must survive into the
application's own file layer (object storage or `upload_file`). Treat old agent
files as expired; the new service starts with a clean workspace. Long-term
delivery should keep using the application's file layer, not `keep/`.

### 6. Verify the cutover

- [ ] The old client is no longer built or imported.
- [ ] `GET /health` and `GET /v1/capabilities` work with the new service.
- [ ] Upload → `Run`/`Exec` with `inputs` → output download works.
- [ ] `OpenPath`, `Archive`, `List` and `DeleteWorkspace` work.
- [ ] A timeout returns `409 timeout` and the command process is gone
      (`CAP_KILL` present).
- [ ] A second identity gets `403` when opening the first identity's file.
- [ ] Old `sbx_` IDs, `X-Tenant-Id` headers and `SANDBOX_RUNNER_*` variables are
      rejected or ignored, as intended.
- [ ] The application's own file layer receives `send_file` artifacts.
- [ ] A runner restart preserves the new workspace and UID assignment.

## Rollback

Keep the old service image, configuration and volume available until the
cutover is accepted. Because there is no dual protocol, rollback means switching
the caller back to the old service; Runbox does not need to understand old
traffic. Do not run both against the same workspace directory.

## Risks

| Risk | Mitigation |
|---|---|
| Old file IDs and agent history disappear | Export artifacts into the application file layer before the switch; accept expired ids. |
| Missing `CAP_KILL` | Add it to the container; verify a timeout kills the process. |
| Identity values contain unsupported characters | Tokens are 1-256 visible ASCII; scoped parts are `[A-Za-z0-9._-]{1,128}`. Encode or reject explicitly. |
| Disk fills during a command | Enforce a volume quota; `RUNBOX_QUOTA_BYTES` only blocks the next command. |
| Multiple replicas | Use sticky routing per identity or one volume per replica. |
| Old service and new service running together | Do not share `/workspace`; the layouts are different. |
