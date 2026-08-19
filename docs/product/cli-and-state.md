# CLI and state contract

## Status

The command surface below is implemented (slice 6, implementation-sequence.md): `cli/cmd/ssh-forward/` builds the CLI binary. `status` / `watch` auto-spawn a per-user manager and then run as its JSON-RPC client; `SSH_FORWARD_NO_AUTOSPAWN=1` keeps the in-process fallback for scripts and tests. `add` and `remove` write `policies.jsonc` and do not require a manager or Development Host. The Manager reads `policies.jsonc` (hot-reloaded) and `config.jsonc`'s `default_host`. `SSH_FORWARD_CONFIG_DIR` overrides the product config directory. `app.Connect` and `app.Serve` are the composition seam where the CLI, a later WebUI, and a later desktop core all land; in-process assembly goes through `core.NewConfiguredManager`.

Still planned: comment-preserving HuJSON patches, idle manager exit, Monitor at Login, and revisioned configuration writes.

## New command contract

The Go CLI is designed independently for this product and has no command, output, file, socket, or runtime-compatibility obligation to any similarly named tool. Its command surface follows the product domain:

```text
ssh-forward add 5173                  # remember a remote port
ssh-forward add --dir /home/dev/app   # remember a Development Host directory
ssh-forward remove 5173
ssh-forward remove --dir /home/dev/app
ssh-forward default <alias>           # pin the default Development Host
ssh-forward status [--json]
ssh-forward watch [--json]            # stream snapshots (JSONL with --json)
ssh-forward policy list [--json]
ssh-forward host list [--json]        # hosts from the SSH client config
ssh-forward manager serve             # run the singleton in the foreground
ssh-forward ui                        # planned: loopback WebUI (slice 7)
```

`add` writes a simple Auto-forward policy (one port, or one working-directory tree) and is idempotent. `remove` forgets that same simple rule. Unmatched listeners are not forwarded; `status` lists new remote ports as a one-line heads-up and lists Local Port Conflicts when allocation could not bind a Local Endpoint. The running Manager applies a saved policy edit against the current observations without waiting for the next scan. The Development Host resolves in order: `--host`, then `config.jsonc`'s `default_host` (set with `ssh-forward default ALIAS`), then the single literal Host alias in the SSH client configuration; with several hosts and no default, a terminal prompts for one per command, and a non-terminal run lists the candidates in the error. There are no legacy numeric shorthands or compatibility aliases. Human-readable output is not an automation contract; every resource command supports structured `--json` output for scripts and desktop clients. `status` and `watch --json` emit the Snapshot codec in `cli/internal/snapshot`, the same shape JSON-RPC embeds.

## Remembered Auto-forward

A remembered port forwards when that remote port has a listener, and does not occupy a local port when it does not. A remembered directory forwards listeners whose process cwd is in that Development Host tree. Both survive manager and SSH restarts because they are policies, not runtime tunnels. `--dir` must be an absolute host path (`/…`), not a path on the Local Machine.

## Persistent intent

The product persists the `default_host` in `config.jsonc` (set with `ssh-forward default`) and Forwarding Policies in `policies.jsonc`. `Monitor at Login`, host lists, product settings, and revisioned configuration writes land with the desktop slice. Runtime tunnels, Listener Observations, Active Forwards, and live connection state remain runtime-only. After restart, policy-driven state is reconstructed from fresh observations rather than restored from a stale runtime snapshot.

## Configuration locations

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/`
- Windows: `%AppData%/ssh-forward/`

`--ssh-config PATH` points the OpenSSH adapter at an explicit client configuration file (default: the user's `~/.ssh/config`); `app` resolves it to an absolute path. `SSH_FORWARD_CONFIG_DIR` overrides the directory for testing and portable operation. `config.jsonc` stores the default host today (a versioned, strict JSONC file read on startup; a corrupt file is diagnosed precisely, not silently ignored); the rest of the host and product settings surface lands with the desktop slice. `policies.jsonc` stores Forwarding Policies, hot-reloaded on a 250ms policy poll — invalid input keeps the last valid set active and sets Policy Diagnostic (`policies_file_invalid`) on the Snapshot. The Manager, Remembered Auto-forward writes, and `policy list` share one `FilePolicyReader`; a mutate of a corrupt file fails without overwriting it.

The planned configuration watch (debounced preview/reconcile of external JSONC edits, revisioned UI writes that refuse to overwrite an external edit, minimal HuJSON patches with atomic replacement and backup) lands with the desktop's configuration surface.

## Manager ownership

Only one manager runs per user (ADR-0016), and the CLI implements it: `ssh-forward manager serve` owns the Manager and listens on the per-user Unix socket (`manager.sock` next to the configuration files, `SSH_FORWARD_CONFIG_DIR`-overridable). `status` and `watch` are then clients of that singleton over the JSON-RPC v1 wire (docs/design/ipc-protocol.md) and share its state; a conflicting `--host` is a warning, not an error. The first `status`/`watch` auto-spawns the singleton in the background (its own executable by absolute path, `manager.log` next to the socket, `manager.pid` recording it) and then executes as its client, so there is no separate start step. Autospawn encodes Serve options as environment (`SSH_FORWARD_MANAGER_SERVE=1`, host, policies path, optional SSH config, and `SSH_FORWARD_CONFIG_DIR`); the child enters `app.Serve` without parsing a Cobra command tree. Foreground `ssh-forward manager serve` still uses Cobra. `SSH_FORWARD_NO_AUTOSPAWN=1` keeps the in-process fallback for scripts and tests, and `SSH_FORWARD_MANAGER_BINARY` overrides the spawned executable. A second serve is refused while one runs; a stale socket file (one no live manager answers) is replaced. Desktop starts its signed bundled core by absolute bundle path rather than searching `$PATH` for a helper. An incompatible client reports the required restart or upgrade and never terminates an unknown manager automatically.

## Compatibility

The product does not inspect, adopt, migrate, stop, replace, or uninstall any unrelated SSH-forwarding utility that happens to share a similar name. There is no compatibility contract with such tools.

## Version boundaries

Product SemVer, manager IPC protocol, JSONC configuration schema, and remote observation protocol are versioned independently. Unsupported protocol versions are rejected explicitly. Configuration migration creates a backup before writing.
