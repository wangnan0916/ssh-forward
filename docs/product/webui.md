# Loopback WebUI

## Implementation status

Planned for slice 7. The CLI (slice 6) is ready. This document is the product spec; `ssh-forward ui` is not in the tree yet. ADR-0021 records the choice over a TUI.

The list layout, remember/forget port, typed port form, and forget confirmation below are the interaction contract. Visual chrome, spacing, and copy may change when slice 7 is implemented.

## Command

```text
ssh-forward ui
```

Same Manager composition as `status` / `watch`: `app.Connect` auto-spawns the per-user singleton when needed. Not the no-arg default. `--json` does not apply. Closing the command stops the HTTP listener; the Manager keeps running.

## Trust

- Bind `127.0.0.1` only. Never a non-loopback address, never `0.0.0.0`.
- Serve on an ephemeral port. The open URL includes an unguessable token; requests without it are refused.
- Do not proxy Manager IPC to the network. The browser talks to this process; this process talks to `manager.sock`.
- Not a remote dashboard. Another machine's browser is out of scope.

## Layout

One table, one type, matching the desktop lists (`docs/product/desktop-experience.md`):

1. **Attention** — unmatched new Remote Listeners and Local Port Conflicts
2. **Active** — working Forwards (remote port, Allocated Local Port, process, directory)
3. **Remembered** — remembered ports with no current listener
4. **Available** — observed listeners that are neither forwarded nor in Attention

Chrome above the table (not a fifth list): host, connection, Discovery, and a **Remember port** / **Forget port** form (digits, 1–65535). Directory remember/forget stays on the CLI for this slice (`add --dir` / `remove --dir`); the table shows directory as evidence only.

Clicking a row (or its action) remembers or forgets that **port**. Forget asks for confirmation. Remember does not. Forget only drops a simple port rule; it does not invent “stop this Forward” when Snapshot has no matching policy.

## Live data

The page replaces its view from Watch Snapshots (full documents, not patches). Local UI state is the selected list, the port form, and a pending forget. Policy writes go through the same `policies.jsonc` path as the CLI; the Manager hot-reloads.

## Out of scope for slice 7

- Pause, reconnect, Ignore, policy editor, multi-host
- Opening the forwarded HTTP URL in a browser (desktop)
- Typed Development Host directory paths
- Embedding this page in SwiftUI
