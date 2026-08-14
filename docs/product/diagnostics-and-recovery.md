# Diagnostics, privacy, and recovery

## Telemetry

The product collects no analytics and uploads no crash report, log, Development Host, Listener Observation, or Process Metadata by default. Any future reporting capability must be explicit opt-in and preview the exact payload before sending.

## Local diagnostics

Structured logs rotate after seven days or 20 MiB, whichever comes first. Normal logs omit full SSH aliases, working directories, argument vectors, environment variables, and authentication prompts. Debug logging is temporary and automatically returns to the normal level. **Export Diagnostics** creates a redacted bundle and previews its contents before writing or sharing it.

## Updates

The private prototype is built and installed manually. An updater dependency such as Sparkle is deferred until external distribution is being prepared. Desktop and manager/core never upgrade independently or silently across incompatible protocol versions.

## Crash recovery

The desktop restarts an unexpectedly exited manager with backoff at most three consecutive times. The manager reconstructs policy-driven behavior from persisted intent and fresh observations; Manual Forwards and one-time decisions are not restored. After repeated failure the desktop stops restarting and exposes diagnostics. A stale Unix socket or named pipe is removed only after proving that no live manager owns it.
