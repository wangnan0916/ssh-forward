---
name: ssh-forward
description: >-
  Operates the ssh-forward CLI: remember a remote port or Development Host
  directory for Auto-forward, pin a default SSH alias, inspect status, or
  start the loopback WebUI.
  Use when forwarding a remote development listener to localhost, running
  ssh-forward, adding or removing a remembered port or --dir, or diagnosing
  why a local port is not reachable.
---

# ssh-forward

Discover eligible TCP listeners on a Linux Development Host and expose them on the Local Machine loopback, preferring the same port. Authentication stays with system OpenSSH and `~/.ssh/config`. This is not a tunnel, credential, key, or host-profile manager.

Use these terms: Development Host, Remote Listener, Local Endpoint, Forwarding Policy, Managed Forward, Allocated Local Port.

Operate only aliases already listed by `ssh-forward host`. Do not edit `~/.ssh/config`, store credentials, or run `ssh -L` as a substitute.

## 1. Check the binary

```bash
ssh-forward --version
```

Done when the command prints a version. If it is missing, stop and tell the user how to install — do not install for them:

```bash
brew install --HEAD wangnan0916/ssh-forward/ssh-forward
```

```bash
go install github.com/wangnan0916/ssh-forward/cli/cmd/ssh-forward@main
```

Needs a system OpenSSH client. The Local Machine is macOS or Linux; Windows is not supported.

## 2. Name the Development Host

```bash
ssh-forward host --json
```

Resolution order: `--host`, then `config.jsonc`'s `default_host`, then the single literal Host alias in the SSH client config. Several hosts and no default: a terminal prompts (number or full alias) and pins that choice as `default_host`. A non-terminal run lists candidates in the error (no interactive prompt). Then either:

```bash
ssh-forward default ALIAS
```

or pass `--host ALIAS` on later commands. `-h` is help, not the host flag. `ssh-forward default` with no alias prints the pinned host.

Done when one alias is pinned or passed, and it appears in `host`.

## 3. Remember, forget, inspect

Resource commands take `--json`. Human-readable output is not an automation contract.

`add` / `remove` write `policies.jsonc` and do not need a manager or a live SSH session. `status` auto-spawns the per-user manager.

```bash
ssh-forward add 5173 --json
ssh-forward add --dir /home/dev/src/app --json
ssh-forward remove 5173 --json
ssh-forward remove --dir /home/dev/src/app --json
ssh-forward policy --json
ssh-forward status --json
```

`--dir` must be an absolute path on the Development Host (`/…`), not a path on the Local Machine.

`add` is idempotent. `remove` fails if that simple port or directory rule is not remembered. `add`/`remove` only create or drop those simple Auto-forward rules; hand-edited policies with extra matchers are left alone.

Do not run `ssh-forward watch` or `ssh-forward manager serve` unless the user asked: `watch` streams until interrupt; `serve` holds the singleton in the foreground. `manager stop` / `manager restart` interrupt Active Forwards; run them only when the user asked to recover the singleton (CLI upgrade, incompatible manager, leftover runtime forwards). There is no TUI.

When the user wants the loopback page:

```bash
ssh-forward ui
ssh-forward ui status --json
```

`ui` prints `http://127.0.0.1:PORT/?token=…` (and opens a browser unless `SSH_FORWARD_UI_NO_OPEN=1`). The page keeps that secret as a cookie and may drop the query from the address bar; later visits still need that printed URL. It is idempotent while that process is running. `ui stop` ends only the page; Active Forwards and the Manager keep running. Do not run `ui` unless the user asked for the WebUI. `ssh-forward ui` starts the background page; it is not a blocking TUI.

## 4. Report live state

After `add`, run `status --json`. A remembered rule is persistent intent. An Active Forward exists only while a matching Remote Listener is observed.

Read:

- `host.connection` and `host.discovery.diagnostic` for SSH / discovery health
- `host.forwards[].allocated_local_port` for the URL to open (`http://127.0.0.1:<port>`)
- `host.local_port_conflicts` when a Local Endpoint could not bind
- `host.listener_observations` whose `remote_port` is not in `forwards`: unmatched listeners, not yet forwarded

Tell the user the Allocated Local Port from the snapshot. It may differ from the remote port: same-port is tried first, then `remotePort+1` through `remotePort+100`. Exhausting that range is a Local Port Conflict; never kill the process occupying a candidate port.

Unmatched loopback Remote Listeners stay visible and unforwarded until a policy matches. Offer `ssh-forward add PORT` for those ports. Do not offer `add` for wildcard listeners (already on the Development Host).

One manager owns one Development Host. A conflicting `--host` on a client command is a warning, not an error.

## Policies on disk

`add`/`remove` are enough for a remembered port or directory tree. Hand-edit `policies.jsonc` only for `ignore`, `bind_scope`, executable, or ancestor matchers.

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/` (or `~/.config/ssh-forward/`)

The manager hot-reloads policies. Invalid input keeps the last valid set in the Manager process. A CLI/WebUI process that never parsed a valid file does not invent remembered ports from the corrupt file.
