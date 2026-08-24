---
name: ssh-forward
description: >-
  Operates ssh-forward for a Linux SSH host: list remote loopback listeners,
  remember a port, maintain automatic working-directory glob rules, select the
  default SSH alias, inspect forwarding status, or trigger automatic manager
  recovery. Use when remote development ports should be reachable on localhost.
---

# ssh-forward

Expose selected IPv4 loopback TCP listeners from one Linux Development Host on
the same localhost port. System OpenSSH and `~/.ssh/config` own authentication
and connection settings.

## 1. Verify the CLI and host

```bash
ssh-forward --version
ssh-forward host --json
ssh-forward default
```

Use an alias returned by `host`. If no default is set, pin one:

```bash
ssh-forward default ALIAS
```

`--host ALIAS` selects an alias for a command; `-h` shows help. If the binary
is missing, report these installation choices and stop:

```bash
brew install --HEAD wangnan0916/ssh-forward/ssh-forward
go install github.com/wangnan0916/ssh-forward/cli/cmd/ssh-forward@main
```

This step is complete when an existing alias is pinned or explicitly selected.

## 2. Inspect and change forwarding intent

Read live state first:

```bash
ssh-forward status --json
```

The command starts the per-user background manager when necessary. In its
result:

- `listeners` contains remote ports currently listening;
- `forwards[].state` is `starting`, `active`, or `failed`;
- an `active` port is reachable at `127.0.0.1:PORT`;
- `discovery.diagnostic` and `forwards[].diagnostic` explain failures.

Remember or forget exactly the port requested:

```bash
ssh-forward add PORT --json
ssh-forward remove PORT --json
```

`add` is idempotent. `remove` reports an error when the port was not
remembered. After a change, poll `status --json` until the relevant forward
reaches `active` or `failed`. Report that state and, when active, the URL or
endpoint using the same port.

Maintain an absolute remote working-directory glob when the user asks for
automatic, listener-scoped forwarding:

```bash
ssh-forward add --pwd '/home/me/Workspace/**' --json
ssh-forward remove --pwd '/home/me/Workspace/**' --json
```

Quote patterns so the local shell does not expand them. `*` stays within one
path segment; `**` crosses directories. A matching Remote Listener creates an
`automatic` Forward, and that Forward stops when a complete listener snapshot
no longer matches. Missing working-directory metadata never matches. A
Remembered Port remains forwarded if the same port also matches a rule.

After adding a rule, confirm it appears in `working_directory_rules` from
`status --json`. If a listener currently matches, also poll until its Forward
is `active` or `failed`. After removing a rule, confirm it is absent and any
Forward selected only by that rule disappears.

## 3. Recover only when needed

Run `ssh-forward status --json` again. The command installs, starts, repairs,
or switches the background manager automatically. For an explicit continuous
view, use `ssh-forward status --watch --json`. If automatic recovery fails,
report the command error instead of editing service-manager state directly.

Persistent intent lives in one `config.jsonc`. Runtime process IDs and listener
observations are rebuilt rather than persisted.

## 4. Remove only on an explicit request

When the user explicitly asks to uninstall ssh-forward, run:

```bash
ssh-forward uninstall
```

This removes the background Manager and keeps remembered Host and port intent.
Report that a package-manager uninstall may now safely remove the binary.
