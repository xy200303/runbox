# Contributing

Thanks for taking a look. This project is intentionally small. The public SDK
is stdlib-only; the HTTP server uses Gin. Keep new dependencies out of
`sandbox/` unless there is a strong reason.

## Development

```bash
go run ./cmd/runboxd          # anonymous mode is off by default
RUNBOX_ALLOW_ANONYMOUS=1 RUNBOX_ROOT=./.runbox go run ./cmd/runboxd
```

On Windows and macOS the runner cannot switch UIDs. It still runs commands as
the current user for development and tests; use the Docker image for any
security-relevant check.

## Before opening a pull request

```bash
make check
```

That runs a gofmt check, `go vet ./...` and `go test -race ./...`.

Security-sensitive paths have tests; please add one when you change:

- `sandbox/fileid.go` — file-id parsing and path validation;
- `internal/runner/paths.go` — symlink and traversal handling;
- `internal/runner/exec.go` — environment, output caps, timeouts;
- `internal/server/` — authentication, identity and body limits.

## Design constraints

- The public SDK (`sandbox/`) must not import `internal/`.
- The runner must not depend on an application framework; HTTP is the only
  integration surface.
- Never let a symlink escape the workspace when serving, archiving or
  collecting files.
- Commands run with a minimal environment. Never pass the server's environment
  through: it contains the API key.
- Timeouts must actually terminate the command; deployments grant `CAP_KILL`.
- Do not add backward-compatibility shims. Break the contract cleanly, update
  the client and server together, and document breaking changes.

## Commit messages

Short imperative subject, then a body explaining the why. No generated files.

## Releases

1. Update `CHANGELOG.md`.
2. Tag the release (`vX.Y.Z`) and push the tag.
3. Build images with `make docker docker-minimal`.

