---
name: ssh-forward
description: >-
  Operates ssh-forward between a local development machine and a Linux SSH
  host: import remote loopback services to localhost, publish local TCP services
  to remote loopback, maintain forwarding intent, select the SSH alias, inspect
  directional status, or trigger Manager recovery. Use when either machine
  needs to reach a loopback TCP service on the other through SSH.
---

# ssh-forward

Keep explicit IPv4 loopback TCP services reachable in either direction between
the local machine and one Linux Development Host. System OpenSSH and
`~/.ssh/config` own authentication and connection settings.

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

## 2. Inspect directional state

Read live state first:

```bash
ssh-forward status --json
```

The command starts the per-user background Manager when necessary. Interpret
each `forwards` entry by direction:

- `listeners` contains remote ports currently listening;
- `forwards[].state` is `starting`, `active`, or `failed`;
- `local_to_remote` publishes the Local Service at Development Host
  `127.0.0.1:remote_port`;
- every other entry is `remote_to_local` for compatibility; its local endpoint
  is `127.0.0.1:local_port`, or `127.0.0.1:port` in the legacy shape;
- `discovery.diagnostic` and `forwards[].diagnostic` explain failures.

For a Published Forward, `active` means sshd installed its remote listener; the
Local Service may still be stopped. Report which machine owns every endpoint.

This step is complete when the relevant direction, source service, listening
endpoint, and state are identified.

## 3. Change forwarding intent

Choose commands from the direction the user requested.

### Development Host service to local machine

Remember or forget the remote port:

```bash
ssh-forward add PORT --json
ssh-forward remove PORT --json
```

`add` is idempotent. `remove` reports an error when the port was not
remembered. After a change, poll `status --json` until the relevant forward
reaches `active` or `failed`. Report that state and, when active, the URL or
endpoint using its reported local port.

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

### Local service to Development Host

Publish the Local Service on the same remote port or an explicit one:

```bash
ssh-forward publish LOCAL --json
ssh-forward publish LOCAL --remote REMOTE --json
ssh-forward unpublish LOCAL --json
```

`publish` is idempotent. `unpublish` reports an error when the local port was
not published. After publishing, poll `status --json` for the
`local_to_remote` entry with that `local_port` until it reaches `active` or
`failed`. When active, report Development Host `127.0.0.1:remote_port` as the
listening endpoint and local `127.0.0.1:local_port` as its target. After
unpublishing, confirm that entry disappears.

This step is complete when status reflects the requested persistent intent and
the direction-aware endpoint is reported.

## 4. Recover only when needed

Run `ssh-forward status --json` again. The command installs, starts, repairs,
or switches the background manager automatically. For an explicit continuous
view, use `ssh-forward status --watch --json`. If automatic recovery fails,
report the command error instead of editing service-manager state directly.

Persistent intent lives in one `config.jsonc`. Runtime process IDs and listener
observations are rebuilt rather than persisted.

## 5. Remove only on an explicit request

When the user explicitly asks to uninstall ssh-forward, run:

```bash
ssh-forward uninstall
```

This removes the background Manager and keeps Host, imported, published, and
working-directory intent. Report that a package-manager uninstall may now
safely remove the binary.
