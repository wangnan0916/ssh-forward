# CLI and state contract

## Status

The command surface below is implemented (slice 6, implementation-sequence.md): `cli/cmd/ssh-forward/` builds the CLI binary, which runs the headless Manager in-process and exposes the domain command surface with wire-shaped `--json` output. The state layout (configuration locations, `policies.jsonc`, `config.jsonc`) is the **planned product contract** and lands progressively with the CLI and desktop slices; today the Manager reads `policies.jsonc` (with `SSH_FORWARD_CONFIG_DIR` override) and the integration tests consume the Manager over the IPC wire. `app.NewManager` in the Go module is the composition seam where the CLI entry point and the desktop core both land.

## New command contract

The Go CLI is designed independently for this product and has no command, output, file, socket, or runtime-compatibility obligation to the pre-existing shell utility. Its command surface follows the product domain:

```text
ssh-forward add 5173                  # forward one remote port
ssh-forward remove 8000               # tear down by port (the natural counterpart of add)
ssh-forward remove manual:cli-xxx     # or by explicit forward ID (from status --json)
ssh-forward approve 8080              # One-time Approval for a Listener
ssh-forward suppress 8080             # One-time Suppression for a Listener
ssh-forward default <alias>           # pin the default Development Host
ssh-forward status [--json]
ssh-forward watch [--json]            # stream snapshots (JSONL with --json)
ssh-forward policy list [--json]
ssh-forward host list [--json]        # hosts from the SSH client config
ssh-forward manager serve             # run the singleton in the foreground
```

The commands are verbs: `add` can only name a remote port, so the port is a positional argument (flags may precede or follow it). The Development Host resolves in order: `--host`, then `config.jsonc`'s `default_host` (set with `ssh-forward default ALIAS`), then the single literal Host alias in the SSH client configuration; with several hosts and no default, a terminal prompts for one per command, and a non-terminal run lists the candidates in the error. There are no legacy numeric shorthands or compatibility aliases. Human-readable output is not an automation contract; every resource command supports structured `--json` output for scripts and desktop clients.

## Manual Forward

A Manual Forward targets only loopback on one Development Host; it cannot name an arbitrary remote destination. `family` may be `auto`, `ipv4`, or `ipv6`. Auto uses a current Listener Observation when available and otherwise defaults to remote IPv4 loopback. Creating a Manual Forward lazily connects its host, binds both supported local loopback families, and applies the normal preferred-port/fallback policy.

## Persistent intent

The product persists Development Host aliases (the `default_host` in `config.jsonc` is read today and names the host when `--host` is absent), `Monitor at Login`, Forwarding Policies, and product settings. The remaining write paths — host lists, settings, and the revisioned configuration updates — land with the desktop slice. Manual Forwards, One-time Approvals, One-time Suppressions, Listener Observations, Active Forwards, and live connection state remain runtime-only. After restart, policy-driven state is reconstructed from fresh observations rather than restored from a stale runtime snapshot.

## Configuration locations

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/`
- Windows: `%AppData%/ssh-forward/`

`--ssh-config PATH` points the OpenSSH adapter at an explicit client configuration file (default: the user's `~/.ssh/config`); the composition root resolves it to an absolute path. `SSH_FORWARD_CONFIG_DIR` overrides the directory for testing and portable operation. `config.jsonc` stores the default host today (a versioned, strict JSONC file read on startup; a corrupt file is diagnosed precisely, not silently ignored); the rest of the host and product settings surface lands with the desktop slice. `policies.jsonc` stores Forwarding Policies, hot-reloaded on the reconciliation cadence (~2s) — invalid input keeps the last valid set active.

The planned configuration watch (debounced preview/reconcile of external JSONC edits, revisioned UI writes that refuse to overwrite an external edit, minimal HuJSON patches with atomic replacement and backup) lands with the desktop's configuration surface.

## Manager ownership

Only one new-product manager runs per user (ADR-0016), and the CLI implements it: `ssh-forward manager serve` owns the Manager and listens on the per-user Unix socket (`manager.sock` next to the configuration files, `SSH_FORWARD_CONFIG_DIR`-overridable). Every other command is then a client of that singleton over the JSON-RPC v1 wire (docs/design/ipc-protocol.md) and shares its state; a conflicting `--host` is a warning, not an error. The first command auto-spawns the singleton in the background (its own executable by absolute path, `manager.log` next to the socket, `manager.pid` recording it) and then executes as its client, so there is no separate start step; `SSH_FORWARD_NO_AUTOSPAWN=1` keeps the in-process fallback for scripts and tests, and `SSH_FORWARD_MANAGER_BINARY` overrides the spawned executable. A second serve is refused while one runs; a stale socket file (one no live manager answers) is replaced. Desktop starts its signed bundled core by absolute bundle path rather than searching `$PATH` for a helper. An incompatible client reports the required restart or upgrade and never terminates an unknown manager automatically.

## Development isolation

Until the new product is mature, its executable path, configuration directory, runtime endpoint, and SSH/SOCKS processes remain isolated from the installed legacy utility. The product does not inspect, adopt, migrate, stop, replace, or uninstall legacy state. The user will remove the old utility separately after choosing to cut over.

## Version boundaries

Product SemVer, manager IPC protocol, JSONC configuration schema, and remote observation protocol are versioned independently. Unsupported protocol versions are rejected explicitly. Configuration migration creates a backup before writing.
