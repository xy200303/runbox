# Changelog

## 0.1.0 — 2026-10-31

Initial extraction from the property-management platform's sandbox module.

- HTTP runner (`runboxd`) with `/v1/exec`, `/v1/files`, `/v1/files/{id}/keep`,
  `/v1/path`, `/v1/list`, `/v1/workspace`, `/v1/capabilities` and `/health`.
- Per-identity workspace, persisted UID assignment and per-identity
  serialization.
- Upload, download, path download, directory listing, immutable archive and
  workspace deletion. Deletion keeps `keep/` by default; `?purge=true` removes
  archived deliverables too.
- Bounded stdout/stderr capture, output caps, workspace and archive quotas,
  configurable concurrency, timeouts and retention.
- Timeouts kill the command's process group; deployments grant `CAP_KILL`, and
  the runtime images use tini as PID 1 to reap the killed tree.
- Symlink-aware path resolution; archive outside the writable workspace;
  workspace subdirectory repair.
- Minimal command environment; no server secret leakage.
- Stdlib-only Go SDK and HTTP client.
- No compatibility layer: canonical `X-Runbox-*` identity headers only,
  `RUNBOX_*` environment only.
- Unified identity model: single token (`X-Runbox-Identity`) or scoped
  namespace + principal (`X-Runbox-Namespace`/`X-Runbox-Principal`), both
  normalized to a canonical SHA-256 key. Ambiguous/partial identities are
  rejected. Scoped metadata is stored in `state/identities/<key>.json`.
- Versioned file IDs: `rbx_` + (version, identity key, kind, path).
- Authentication is Bearer-only (`Authorization: Bearer`); `X-API-Key` is
  rejected.
- Single-container, multi-identity design (dify-sandbox shape) with a
  per-identity file isolation layer: workspace, quota, immutable archive and
  symlink-safe paths.
- Fail-closed Linux capability check; `RUNBOX_ALLOW_MISSING_CAPS` is a
  development-only override.
- `/ready` storage readiness and `/metrics` Prometheus counters.
- Bounded command length and input count (`RUNBOX_MAX_COMMAND_BYTES`,
  `RUNBOX_MAX_INPUTS`).
- Graceful shutdown cancels in-flight commands after the configured budget.
- Fuzz tests for file IDs and paths; boundary tests for nested outputs, output
  caps, upload overwrite, concurrent identities and readiness.
- Full image with LibreOffice/Python/Node and a minimal image.
- Read-only compose deployment with dropped capabilities and resource limits.
- Bilingual README, API/architecture/security/deployment/migration docs,
  Go/Python/curl examples and CI.
