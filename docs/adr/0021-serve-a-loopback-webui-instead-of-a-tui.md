# Serve a loopback WebUI instead of a TUI

Slice 7 is a local-machine WebUI, not a terminal UI. `ssh-forward ui start` launches a background loopback HTTP process that is a JSON-RPC client of the per-user Manager (`app.Connect`), the same seam as `status` / `watch`. `ui status` prints the URL; `ui stop` ends the process. The listener binds only `127.0.0.1`, requires a secret in the printed URL (later the same secret as a host-only cookie), and `start` opens the system browser when it can. Remember / forget use the existing policy writers (`add` / `remove`); the page does not become a second Manager. Stopping the UI does not stop the Manager. There is no `ssh-forward tui` and no Bubble Tea dependency. The macOS desktop remains a later native SwiftUI surface (ADR-0002); whether it later embeds this page is a separate decision.

## Considered Options

- Charm Bubble Tea TUI as slice 7 — rejected: tabbed tables, typed ports, and confirmations are slower to design and operate in a terminal than on a page.
- Blocking `ssh-forward ui` that owns the terminal until Ctrl-C — rejected: the operator would have to keep a shell open for the whole time they want the page; start/status/stop matches how they already think about the Manager.
- Skip an interactive GUI and start SwiftUI next — rejected: the desktop is macOS-only; a loopback WebUI covers macOS and Linux Local Machines with one page.
- Listen beyond loopback, or serve the UI to another machine — rejected: the threat model does not trust unauthenticated network input; Manager IPC stays on the Unix socket.
- Replace the planned SwiftUI desktop with this WebUI (Electron / WKWebView) in the same slice — deferred: ADR-0002 still holds for the menu-bar app.
