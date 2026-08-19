# CLI and state contract

## Status

The command surface below is implemented (slices 6–7, implementation-sequence.md): `cli/cmd/ssh-forward/` builds the CLI binary. `status` / `watch` auto-spawn a per-user manager and then run as its JSON-RPC client; `SSH_FORWARD_NO_AUTOSPAWN=1` keeps the in-process fallback for scripts and tests. `add` and `remove` write `policies.jsonc` and do not require a manager or Development Host. The Manager reads `policies.jsonc` (hot-reloaded) and `config.jsonc`'s `default_host`. `SSH_FORWARD_CONFIG_DIR` overrides the product config directory. `app.Connect` and `app.Serve` are the composition seam where the CLI, the loopback WebUI, and a later desktop core all land; in-process assembly goes through `core.NewConfiguredManager`.

Still planned: comment-preserving HuJSON patches, idle manager exit, Monitor at Login, and revisioned configuration writes.

## New command contract

The Go CLI is designed independently for this product and has no command, output, file, socket, or runtime-compatibility obligation to any similarly named tool. Its command surface follows the product domain:

```text
ssh-forward                           # grouped primer (exit 0; does not connect)
ssh-forward add 5173                  # remember a remote port
ssh-forward add --dir /home/dev/app   # remember a Development Host directory
ssh-forward remove 5173
ssh-forward remove --dir /home/dev/app
ssh-forward default                   # show the pinned host
ssh-forward default ALIAS             # pin the default Development Host
ssh-forward status [--json] [--watch]
ssh-forward watch [--json]            # stream snapshots (JSONL with --json)
ssh-forward policy [--json]           # list policies (`policy list` is an alias)
ssh-forward host [--json]             # hosts from the SSH client config (`host list` is an alias)
ssh-forward manager serve             # run the singleton in the foreground
ssh-forward manager stop              # stop the singleton (recovery)
ssh-forward manager restart           # stop, then auto-spawn again
ssh-forward ui                        # open the loopback page (`ui start` is an alias)
ssh-forward ui status [--json]
ssh-forward ui stop
```

`add` writes a simple Auto-forward policy (one port, or one working-directory tree) and is idempotent. `remove` forgets that same simple rule. Unmatched listeners are not forwarded; human `status` groups Forwards, Waiting, Available (unmatched **loopback** ports with `ssh-forward add PORT`), and Needs attention (Local Port Conflicts). Wildcard listeners are already on the Development Host and are omitted from Available (`status --json` still has the full Snapshot). The running Manager applies a saved policy edit against the current observations without waiting for the next scan. The Development Host resolves in order: `--host`, then `config.jsonc`'s `default_host` (set with `ssh-forward default ALIAS`, or by picking one at a terminal prompt — number or full alias), then the single literal Host alias in the SSH client configuration; with several hosts and no default, a terminal prompts once and pins that choice, and a non-terminal run lists the candidates in the error. `-h` is help; name the host with `--host`. On a terminal, human `status` waits until SSH has connected (or failed) and discovery has a first result; `--json` prints the current snapshot with no wait; `--watch` streams like `watch`. `ssh-forward` with no subcommand prints a grouped primer and exits 0. `host`, `policy`, and `ui` with no subcommand do their one useful action. There are no legacy numeric shorthands or compatibility aliases. Human-readable output is not an automation contract; every resource command supports structured `--json` output for scripts and desktop clients. `status` and `watch --json` emit the Snapshot codec in `cli/internal/snapshot`, the same shape JSON-RPC embeds.

## Remembered Auto-forward

A remembered port forwards when that remote port has a listener, and does not occupy a local port when it does not. A remembered directory forwards listeners whose process cwd is in that Development Host tree. Both survive manager and SSH restarts because they are policies, not runtime tunnels. `--dir` must be an absolute host path (`/…`), not a path on the Local Machine.

## Persistent intent

The product persists the `default_host` in `config.jsonc` (set with `ssh-forward default`, or by picking a host at a terminal prompt) and Forwarding Policies in `policies.jsonc`. `Monitor at Login`, host lists, product settings, and revisioned configuration writes land with the desktop slice. Runtime tunnels, Listener Observations, Active Forwards, and live connection state remain runtime-only. After restart, policy-driven state is reconstructed from fresh observations rather than restored from a stale runtime snapshot.

## Configuration locations

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/`
- Windows: `%AppData%/ssh-forward/`

`--ssh-config PATH` points the OpenSSH adapter at an explicit client configuration file (default: the user's `~/.ssh/config`); `app` resolves it to an absolute path. `SSH_FORWARD_CONFIG_DIR` overrides the directory for testing and portable operation. `config.jsonc` stores the default host today (a versioned, strict JSONC file read on startup; a corrupt file is diagnosed precisely, not silently ignored); the rest of the host and product settings surface lands with the desktop slice. `policies.jsonc` stores Forwarding Policies, hot-reloaded on a 250ms policy poll — invalid input keeps the last valid set active and sets Policy Diagnostic (`policies_file_invalid`) on the Snapshot. The Manager, Remembered Auto-forward writes, and `policy list` share one `FilePolicyReader`; a mutate of a corrupt file fails without overwriting it.

The planned configuration watch (debounced preview/reconcile of external JSONC edits, revisioned UI writes that refuse to overwrite an external edit, minimal HuJSON patches with atomic replacement and backup) lands with the desktop's configuration surface.

## Manager ownership

Only one manager runs per user (ADR-0016), and the CLI implements it: `ssh-forward manager serve` owns the Manager and listens on the per-user Unix socket (`manager.sock` next to the configuration files, `SSH_FORWARD_CONFIG_DIR`-overridable). `status` and `watch` are then clients of that singleton over the JSON-RPC v1 wire (docs/design/ipc-protocol.md) and share its state; a conflicting `--host` is a warning, not an error. The first `status`/`watch` auto-spawns the singleton in the background (its own executable by absolute path, `manager.log` next to the socket, `manager.pid` recording it) and then executes as its client, so there is no separate start step. Autospawn encodes Serve options as environment (`SSH_FORWARD_MANAGER_SERVE=1`, host, policies path, optional SSH config, and `SSH_FORWARD_CONFIG_DIR`); the child enters `app.Serve` without parsing a Cobra command tree. Foreground `ssh-forward manager serve` still uses Cobra. `SSH_FORWARD_NO_AUTOSPAWN=1` keeps the in-process fallback for scripts and tests, and `SSH_FORWARD_MANAGER_BINARY` overrides the spawned executable. A second serve is refused while one runs; a stale socket file (one no live manager answers) is replaced. `ssh-forward manager stop` SIGTERMs only the pid recorded in `manager.pid` after that process still answers the singleton socket; a live pid that does not own the socket is left alone. `ssh-forward manager restart` is stop followed by the same auto-spawn as `status` (cold start still needs `--host` or `default`). Desktop starts its signed bundled core by absolute bundle path rather than searching `$PATH` for a helper. An incompatible client reports the required restart (`ssh-forward manager restart`) and never terminates an unknown manager automatically.

`ssh-forward ui start` is a second per-user background process: it binds loopback HTTP, records `ui.pid` / `ui.url` / `ui.log` next to the manager files, and talks to the Manager through `app.Connect`. Autospawn encodes serve options as environment (`SSH_FORWARD_UI_SERVE=1`, host, policies path, optional SSH config, and `SSH_FORWARD_CONFIG_DIR`); the child enters the loopback HTTP path without parsing a Cobra command tree. Foreground `ssh-forward ui serve` still uses Cobra. `ui stop` ends only that process. `SSH_FORWARD_UI_BINARY` overrides the spawned UI executable; `SSH_FORWARD_UI_NO_OPEN=1` skips opening a browser. `app.StartUI` / `StopUI` / `LiveUIURL` own that lifecycle the same way `Connect` / `Stop` own the manager.

## Compatibility

The product does not inspect, adopt, migrate, stop, replace, or uninstall any unrelated SSH-forwarding utility that happens to share a similar name. There is no compatibility contract with such tools.

## Version boundaries

Product SemVer, manager IPC protocol, JSONC configuration schema, and remote observation protocol are versioned independently. Unsupported protocol versions are rejected explicitly. Configuration migration creates a backup before writing.
