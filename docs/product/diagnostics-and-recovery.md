# Diagnostics, privacy, and recovery

## Telemetry

The product collects no analytics and uploads no crash report, log, Development Host, Listener Observation, or Process Metadata by default. Any future reporting capability must be explicit opt-in and preview the exact payload before sending.

## Local diagnostics

Structured logs rotate after seven days or 20 MiB, whichever comes first. Normal logs omit full SSH aliases, working directories, argument vectors, environment variables, and authentication prompts. Debug logging is temporary and automatically returns to the normal level. **Export Diagnostics** creates a redacted bundle and previews its contents before writing or sharing it.

## Updates

The public CLI is built from source or installed as a HEAD Homebrew formula. An automatic updater (for example Sparkle, with the desktop app) is deferred. Desktop and manager/core must never upgrade independently or silently across incompatible protocol versions.

## Crash recovery

The desktop restarts an unexpectedly exited manager with backoff at most three consecutive times. The manager reconstructs policy-driven behavior from persisted intent and fresh observations. After repeated failure the desktop stops restarting and exposes diagnostics. A stale Unix socket or named pipe is removed only after proving that no live manager owns it. The CLI recovery path is explicit: `ssh-forward manager stop` signals only the recorded singleton pid after it still answers the socket; `ssh-forward manager restart` then auto-spawns. An incompatible live manager is reported rather than killed.

## Implementation status

The sections above state the full-product behavior. This map records which behaviors are enforced today and which land with later slices:

Enforced today:

- No analytics, crash reporting, or uploads by default; the product never sends Development Host, Listener Observation, or Process Metadata anywhere.
- The CLI is installed from source or Homebrew HEAD; no updater dependency exists.
- `ssh-forward manager stop` / `restart` recover the per-user singleton; a live pid that does not own the manager socket is not killed. An incompatible live manager is reported (`ssh-forward manager restart`) instead of spawning a second one.

Lands with later slices:

- Structured log rotation (seven days / 20 MiB) and normal-log redaction of aliases, working directories, argument vectors, environment variables, and prompts: with the first log sink (ADR-0010 persistence surfaces; research/library-options.md slog design).
- Export Diagnostics redacted bundle with preview: with the first log sink.
- Desktop restart backoff: desktop phase. The IPC half (ADR-0017) is started: `manager serve` listens on the per-user Unix socket, refuses a second singleton, and replaces a stale socket file only after its connection probe proves no live manager owns it.
