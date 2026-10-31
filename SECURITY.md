# Security policy

Runbox executes untrusted commands. Please treat every report as potentially
critical.

## Reporting a vulnerability

Do not open a public issue. Send a private report to the maintainers through
GitHub's "Report a vulnerability" flow (Security tab) or email the address in
the repository profile. Include a reproduction, the affected version and the
impact you believe it has. We aim to acknowledge within 3 working days.

## What is in scope

- escaping the workspace through paths, symlinks or shell commands;
- reading another identity's files;
- escaping the container or obtaining capabilities it was not given;
- leaking the runner API key or the server process environment into commands;
- denial of service that is not explained by the documented limits.

## What is not a vulnerability by itself

- A command can make outbound network requests when the container has network
  access. Use a network policy if that is not acceptable.
- A command can read world-readable files inside the image (for example
  `/etc/passwd`). The threat model assumes the image is disposable and holds no
  secrets.
- Resource exhaustion up to the configured memory/CPU/pids limits.
- The runner requires root **inside its own container** to assign UIDs. This is
  expected; the compose file drops every capability except CHOWN, FOWNER,
  SETUID, SETGID, DAC_OVERRIDE and KILL. KILL is required so a timeout can
  terminate a command running as another UID. Startup fails closed when a
  required capability is missing; `RUNBOX_ALLOW_MISSING_CAPS=1` is for
  development only.

See [docs/security.md](docs/security.md) for the full threat model.
