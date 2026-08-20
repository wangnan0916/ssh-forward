# Loopback WebUI

## Implementation status

Slice 7 is implemented in the CLI: `ssh-forward ui start` / `status` / `stop`. ADR-0021 records the choice over a TUI. GitHub issue #1 is the original implementation spec.

The list layout, remember/forget port, typed port form, and forget confirmation below are the interaction contract. Visual chrome follows a compact Geist-like dashboard (neutral surfaces, status pills, one primary Remember action).

## Command

```text
ssh-forward ui start
ssh-forward ui status [--json]
ssh-forward ui stop
```

`start` launches a background UI process (at most one per user), prints the loopback URL (token included), and opens the system browser when it can. If the UI is already running, it reprints that URL and succeeds. `status` prints the same URL or fails if nothing is running. `stop` ends the UI process; the Manager keeps running. Closing the terminal that ran `start` does not stop the page. `ssh-forward ui` with no subcommand starts the background page. Not the no-arg default of `ssh-forward`.

The child attaches to the per-user Manager through `app.Connect` the same way `status` / `watch` do (auto-spawn when needed). Runtime files next to the manager socket: `ui.pid`, `ui.url`, `ui.log`. `SSH_FORWARD_UI_NO_OPEN=1` skips opening a browser. `SSH_FORWARD_UI_BINARY` overrides the spawned executable (tests).

## Trust

- Bind `127.0.0.1` only. Never a non-loopback address, never `0.0.0.0`.
- Serve on an ephemeral port. The printed URL includes an unguessable token. The page stores it as a host-only `SameSite=Strict` cookie and drops the query from the address bar; requests still need that cookie or the query token.
- Do not proxy Manager IPC to the network. The browser talks to this process; this process talks to `manager.sock`.
- Not a remote dashboard. Another machine's browser is out of scope.

## Layout

One list, one type, matching the desktop lists (`docs/product/desktop-experience.md`):

1. **Attention** — Local Port Conflicts. Snapshot has no per-listener newness flag, so unmatched observed ports are listed under Available rather than invented as “new.”
2. **Active** — working Forwards (remote port, Allocated Local Port, process, directory)
3. **Remembered** — remembered ports with no current listener
4. **Available** — observed listeners that are neither forwarded nor in Attention. Ignore still appears here (`reason=ignored`); Policy Evidence (`reason`, `policy_id`) lives on these rows, not on the Manager Snapshot.

Chrome above the list (not a fifth list): host, connection, Discovery, Connection / Discovery / Policy Diagnostic as wire-code status pills, and a **Remember port** form with Remember and Forget (digits, 1–65535). Directory remember/forget stays on the CLI for this slice (`add --dir` / `remove --dir`); the list shows directory as evidence only.

Clicking a row (or its action) remembers or forgets that **port**. Forget asks for confirmation. Remember does not. Forget only drops a simple port rule; it does not invent “stop this Forward” when Snapshot has no matching policy.

## Live data

CLI human status and the page share `present.NewDocument` (lists, Addable, Host chrome). The HTTP view is `{revision, host chrome, lists, remembered_ports}`; the page renders that document and does not regroup Snapshot fields. One Manager `Watch` in the UI process fans out to SSE clients.

The page replaces its view from those documents (full documents, not patches). Local UI state is the selected list, the port form, and a pending forget. Policy writes go through `ui.Intent` (`FilePolicyReader` in production); the Manager hot-reloads. A cold UI process with an unreadable policies file does not invent Remembered ports or unmatched Policy Evidence.

## Out of scope for slice 7

- Pause, reconnect, Ignore, policy editor, multi-host
- Opening the forwarded HTTP URL in a browser (desktop)
- Typed Development Host directory paths
- Embedding this page in SwiftUI
