# First public release

The first public release is the headless CLI. It covers a VS Code Ports-style workflow for one Linux Development Host from a macOS or Linux Local Machine.

## Development path

Work proceeds one surface at a time. Do not start the next until the previous is ready.

1. **CLI** (current) — headless command surface in `cli/`.
2. **TUI** — terminal UI on the same Manager and JSON-RPC wire; starts after the CLI is ready.
3. **macOS desktop** — native menu-bar app; starts after the TUI is ready.

TUI and desktop are not in this repository yet.

## Included in the CLI

- One SSH alias (from `~/.ssh/config` or `--host`)
- Agentless Remote Listener discovery with process and working-directory context
- Remembered Auto-forward for a remote port (`add 5173`) or a Development Host directory (`add --dir …`)
- Preferred local port with bounded fallback
- Reconnection and local connection diagnostics
- Domain-oriented CLI with `--json` output

## Planned after the CLI

TUI (after CLI is ready):

- Interactive live view of the Development Host, new remote ports, and Active Forwards
- Remember / forget a port or directory from that view

macOS desktop (after TUI is ready):

- Automatic monitoring at login and idle manager exit
- Menu-bar quick panel and full Dashboard
- Explicit HTTP/HTTPS browser actions
- Configuration editing UI, comment-preserving policy writes, and policy explanation on the Snapshot

## Excluded

- Multi-host aggregate UI (one Manager still owns one Development Host)
- Reverse, dynamic, or arbitrary-destination tunnel management
- Credential/key/profile management
- Accounts, cloud sync, and analytics
- Windows as a Local Machine
- Backward compatibility or migration from any pre-existing shell utility

## Implementation status

The Included list is the first public CLI's scope. This map records which items exist today:

Enforced today:

- Agentless Remote Listener discovery with process and working-directory context (slices 1–4).
- Preferred local port with bounded fallback (ADR-0008, core/forward_ownership.go).
- Reconnection with classified failures.
- Forwarding Policy evaluation with directory, direct-process, and ancestor-process matchers; policies.jsonc (versioned, ADR-0005); unmatched listeners are not forwarded; Managed Forward reconciliation with observation-and-clock hysteresis (slice 5).
- The domain-oriented CLI with `--json` output and a per-user auto-spawned manager (slice 6, cli/cmd/ssh-forward/).
- `add` / `remove` write simple Auto-forward policies to policies.jsonc (port or working-directory tree).

Lands later:

- TUI: after the CLI is ready.
- Login monitoring, idle exit, menu-bar, Dashboard: macOS desktop, after the TUI is ready.
- HTTP/HTTPS browser actions: desktop policy surfaces.
- Policy explanation on the Snapshot and `requireSamePort`: later CLI/core work.
