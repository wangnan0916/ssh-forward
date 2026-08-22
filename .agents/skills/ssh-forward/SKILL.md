---
name: ssh-forward
description: >-
  Operates ssh-forward for a Linux SSH host: list remote loopback listeners,
  remember or forget a port, select the default SSH alias, inspect forwarding
  status, or recover the background manager. Use when a remote development
  port should be reachable on localhost or an ssh-forward port is unavailable.
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

## 2. Inspect and change remembered ports

Read live state first:

```bash
ssh-forward status --json
```

The command starts the per-user background manager when necessary. In its
result:

- `listeners` contains remote ports currently listening;
- `forwards[].state` is `waiting`, `starting`, `active`, or `failed`;
- an `active` port is reachable at `127.0.0.1:PORT`;
- `discovery.diagnostic`, `forwards[].diagnostic`, and `config_diagnostic`
  explain failures.

Remember or forget exactly the port requested:

```bash
ssh-forward add PORT --json
ssh-forward remove PORT --json
```

`add` is idempotent. `remove` reports an error when the port was not
remembered. After a change, poll `status --json` until the relevant forward
reaches `active`, `waiting`, or `failed`. Report that state and, when active,
the URL or endpoint using the same port.

## 3. Recover only when needed

Use `ssh-forward manager restart` for an incompatible or stuck background
manager. It interrupts current forwards. Use `manager serve` only for an
explicit foreground-debugging request, and `watch` only for an explicit
continuous-monitoring request.

Persistent intent lives in one `config.jsonc`. Runtime process IDs and listener
observations are rebuilt rather than persisted.
