# Changelog

## Unreleased

- `full` is now built on `toolbox`, so the default image can compile native
  Python and Node extensions. Its amd64 download grows from ~485 MB to ~600 MB.
- Image sizes in the docs are now the published download size rather than an
  earlier `du` measurement: `minimal` ~170 MB, `toolbox` ~285 MB, `full` ~600 MB.

## 0.2.2 — 2026-10-31

- Renamed `RUNBOX_MAX_INPUTS` to `RUNBOX_MAX_ATTACHMENTS` and the capabilities
  field `max_inputs` to `max_attachments`.
- `POST /v1/files` now accepts only the `file` field; use
  `POST /v1/files/batch` for multiple files.
- Documented `GET /v1/archive` as the batch download entry and the matching SDK
  methods (`DownloadArchive`, `download_archive`, `downloadArchive`).
- Publish the `toolbox` image alongside `full` and `minimal`, and document
  one-line `docker pull`/`docker run` commands plus SDK integration rules.

## 0.2.1 — 2026-10-31

Security patch release.

- Build with Go 1.26.10 and upgrade `golang.org/x/net`, `x/crypto`, `x/sys` and
  `quic-go` to versions that fix the advisories reported by `govulncheck`
  (GO-2026-6605 through GO-2026-6809 and the HTTP/3 QPACK issues).
- Add `security.yml`: `govulncheck`, `npm audit` and a Trivy critical scan on
  push and weekly; add Dependabot for Go, npm, pip, Actions and Docker.
- Container publishing now emits SBOM and provenance attestations.

## 0.2.0 — 2026-10-31

- Removed the immutable `keep/` archive, `POST /v1/files/{id}/keep`,
  `RUNBOX_KEEP_BYTES` and the `?purge` parameter. Callers download what they
  need and persist it themselves.
- Files are addressed by workspace-relative path. Uploads return `path`, exec
  outputs include `path`, and `GET /v1/files/{id}`, the exec `inputs` field and
  the `rbx_` file-id format were removed.

## 0.1.0 — 2026-10-31

Initial extraction from the property-management platform's sandbox module.

- HTTP runner (`runboxd`) with `/v1/exec`, `/v1/files`, `/v1/path`,
  `/v1/list`, `/v1/workspace`, `/v1/capabilities` and `/health`.
- Gin-based HTTP layer with streaming multipart uploads, Range and HEAD
  downloads, and JSON errors for unknown routes and methods.
- SQLite state store (GORM, pure-Go driver) for identities and UID assignments.
- Batch file upload (`POST /v1/files/batch`) and tar archive downloads
  (`GET /v1/archive`) for intermediate files and directories.
- Per-identity workspace, persisted UID assignment and per-identity
  serialization.
- Upload, download, path download, directory listing and workspace deletion.
  Delivered files are downloaded by the caller; there is no second long-term
  copy inside Runbox.
- Bounded stdout/stderr capture, output caps, workspace quota, configurable
  concurrency, timeouts and retention.
- Timeouts kill the command's process group; deployments grant `CAP_KILL`, and
  the runtime images use tini as PID 1 to reap the killed tree.
- Symlink-aware path resolution and workspace subdirectory repair.
- Minimal command environment; no server secret leakage.
- Go, Python and Node SDKs.
- No compatibility layer: canonical `X-Runbox-*` identity headers only,
  `RUNBOX_*` environment only.
- Unified identity model: single token (`X-Runbox-Identity`) or scoped
  namespace + principal (`X-Runbox-Namespace`/`X-Runbox-Principal`), both
  normalized to a canonical SHA-256 key. Ambiguous/partial identities are
  rejected. Identity metadata is stored in `state/runbox.db`.
- Versioned file IDs: `rbx_` + (version, identity key, kind, path); removed in
  0.2.0 in favor of workspace-relative paths.
- Authentication is Bearer-only (`Authorization: Bearer`); `X-API-Key` is
  rejected.
- Single-container, multi-identity design (dify-sandbox shape) with a
  per-identity file isolation layer: workspace, quota and symlink-safe paths.
- Fail-closed Linux capability check; `RUNBOX_ALLOW_MISSING_CAPS` is a
  development-only override.
- `/ready` storage readiness and `/metrics` Prometheus counters.
- Bounded command length and attachment count (`RUNBOX_MAX_COMMAND_BYTES`,
  `RUNBOX_MAX_ATTACHMENTS`).
- Graceful shutdown cancels in-flight commands after the configured budget.
- Fuzz tests for paths and identities; boundary tests for nested outputs, output
  caps, upload overwrite, concurrent identities and readiness.
- Full image with LibreOffice/Python/Node and a minimal image.
- Read-only compose deployment with dropped capabilities and resource limits.
- Bilingual README, API/architecture/security/deployment/migration docs,
  Go/Python/curl examples and CI.
- Go, Python and Node SDKs under `sdk/`.
- Static product website under `website/`, deployed to `runbox.xyun.dev`.
- GitHub Actions for CI, multi-arch GHCR images, SDK releases and Pages.
