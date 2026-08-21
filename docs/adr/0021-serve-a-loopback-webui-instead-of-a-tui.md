# Serve a loopback WebUI instead of a TUI

**Status:** the loopback WebUI has been removed from this tree. The CLI is headless. This ADR still records why slice 7 was not a TUI and why there is no `ssh-forward tui`.

Slice 7 was a local-machine WebUI, not a terminal UI. `ssh-forward ui start` launched a background loopback HTTP process that was a JSON-RPC client of the per-user Manager (`app.Connect`), the same seam as `status` / `watch`. There is no `ssh-forward tui` and no Bubble Tea dependency.

## Considered Options

- Charm Bubble Tea TUI as slice 7 — rejected: tabbed tables, typed ports, and confirmations are slower to design and operate in a terminal than on a page.
- Blocking `ssh-forward ui` that owns the terminal until Ctrl-C — rejected: the operator would have to keep a shell open for the whole time they want the page; start/status/stop matches how they already think about the Manager.
- Skip an interactive GUI and start SwiftUI next — rejected: the desktop is macOS-only; a loopback WebUI covers macOS and Linux Local Machines with one page.
- Listen beyond loopback, or serve the UI to another machine — rejected: the threat model does not trust unauthenticated network input; Manager IPC stays on the Unix socket.
- Replace the planned SwiftUI desktop with this WebUI (Electron / WKWebView) in the same slice — deferred: ADR-0002 still holds for the menu-bar app.
