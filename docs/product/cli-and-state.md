# CLI and state contract

## Status

The command surface below is implemented (slice 6, implementation-sequence.md): `cli/cmd/ssh-forward/` builds the CLI binary, which runs the headless Manager in-process and exposes the domain command surface with wire-shaped `--json` output. The state layout (configuration locations, `policies.jsonc`, `config.jsonc`) is the **planned product contract** and lands progressively with the CLI and desktop slices; today the Manager reads `policies.jsonc` (with `SSH_FORWARD_CONFIG_DIR` override) and the integration tests consume the Manager over the IPC wire. `app.NewManager` in the Go module is the composition seam where the CLI entry point and the desktop core both land.

## New command contract

The Go CLI is designed independently for this product and has no command, output, file, socket, or runtime-compatibility obligation to the pre-existing shell utility. Its command surface follows the product domain:

```text
ssh-forward host ...
ssh-forward listener ...
ssh-forward forward ...
ssh-forward policy ...
ssh-forward status
ssh-forward manager ...
```

There are no legacy numeric shorthands or compatibility aliases. Human-readable output is not an automation contract; every resource command supports structured `--json` output for scripts and desktop clients.

## Manual Forward

A Manual Forward targets only loopback on one Development Host; it cannot name an arbitrary remote destination. `family` may be `auto`, `ipv4`, or `ipv6`. Auto uses a current Listener Observation when available and otherwise defaults to remote IPv4 loopback. Creating a Manual Forward lazily connects its host, binds both supported local loopback families, and applies the normal preferred-port/fallback policy.

## Persistent intent

The product persists Development Host aliases, default-host selection, `Monitor at Login`, Forwarding Policies, and product settings. Manual Forwards, One-time Approvals, One-time Suppressions, Listener Observations, Active Forwards, and live connection state remain runtime-only. After restart, policy-driven state is reconstructed from fresh observations rather than restored from a stale runtime snapshot.

## Configuration locations

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/`
- Windows: `%AppData%/ssh-forward/`

`SSH_FORWARD_CONFIG_DIR` overrides the directory for testing and portable operation. `config.jsonc` stores host and product settings; `policies.jsonc` stores Forwarding Policies.

The manager watches external JSONC changes with debounce. A valid change is previewed and reconciled; invalid input leaves the last valid configuration active and surfaces precise diagnostics. UI writes carry a configuration revision and refuse to overwrite an external edit. Product writes use minimal HuJSON patches, atomic replacement, and backup.

## Manager ownership

Only one new-product manager runs per user. Desktop starts its signed bundled core by absolute bundle path; the planned standalone CLI starts its own installed executable rather than searching `$PATH` for a helper. Compatible clients reuse the running manager. An incompatible client reports the required restart or upgrade and never terminates an unknown manager automatically.

## Development isolation

Until the new product is mature, its executable path, configuration directory, runtime endpoint, and SSH/SOCKS processes remain isolated from the installed legacy utility. The product does not inspect, adopt, migrate, stop, replace, or uninstall legacy state. The user will remove the old utility separately after choosing to cut over.

## Version boundaries

Product SemVer, manager IPC protocol, JSONC configuration schema, and remote observation protocol are versioned independently. Unsupported protocol versions are rejected explicitly. Configuration migration creates a backup before writing.
