# Contributing

Thanks for taking a look. This project is intentionally small. The public SDK
is stdlib-only; the HTTP server uses Gin. Keep new dependencies out of
`sdk/go/` unless there is a strong reason.

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
make sdk-test
make website-build
```

`make check` runs a gofmt check, `go vet ./...` and `go test -race ./...`.
`sdk-test` runs the Python and Node suites; `website-build` builds the site.

Security-sensitive paths have tests; please add one when you change:

- `sdk/go/paths.go` — path normalization and file-name sanitization;
- `internal/runner/paths.go` — symlink and traversal handling;
- `internal/runner/exec.go` — environment, output caps, timeouts;
- `internal/server/` — authentication, identity and body limits.

## Design constraints

- The public SDK (`sdk/go/`) must not import `internal/`.
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

Use Conventional Commits: `feat(scope):`, `fix(scope):`, `docs:`, `build:`,
`ci:`, `refactor:`, `test:` or `chore:`. Keep the subject imperative and add a
body explaining the why when the change is not obvious.

## Releases

1. Update `CHANGELOG.md`.
2. Tag the release (`vX.Y.Z`) and push the tag.
3. `docker.yml` publishes multi-arch images to GHCR, `release.yml` creates the
   GitHub release, and the Python/npm jobs publish when `PYPI_API_TOKEN` and
   `NPM_TOKEN` are configured.
4. The website deploys from `main` through `pages.yml`.
5. To publish the Python/npm packages independently, add `PYPI_API_TOKEN` and
   `NPM_TOKEN` repository secrets and run the `sdk-publish` workflow.

