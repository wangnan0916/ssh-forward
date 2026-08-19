# ssh-forward

Discover ports on a Linux development host and use them on localhost through your existing OpenSSH setup. Same port locally when it is free.

[![CI](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml/badge.svg)](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

The first public release is the **CLI**. Next is a loopback WebUI (`ssh-forward ui`, after the CLI is ready), then a macOS menu-bar app (after the WebUI is ready). Neither later surface is in this repository yet. There is no TUI.

This is not a general SSH tunnel, credential, key, or host-profile manager. Authentication stays with system OpenSSH and `~/.ssh/config`.

## Why

- **Remember a port.** `add 5173` auto-forwards that remote port when a service is listening. When nothing is listening, it does not occupy a local port. The rule survives manager and SSH restarts.
- **Remember a project.** `add --dir /home/dev/src/app` auto-forwards listeners whose process working directory is in that tree on the Development Host (not a path on your Mac).
- **System OpenSSH.** The product launches the platform `ssh` binary. It does not embed SSH, store passwords, or replace your SSH config.

## Install

Needs a system OpenSSH client.

```bash
brew install --HEAD wangnan0916/ssh-forward/ssh-forward
```

Or with [Go](https://go.dev/dl/) 1.26 or newer:

```bash
go install github.com/wangnan0916/ssh-forward/cli/cmd/ssh-forward@main
```

```bash
git clone https://github.com/wangnan0916/ssh-forward.git
cd ssh-forward/cli
go build -o ssh-forward ./cmd/ssh-forward
```

## Quick start

The Development Host is an SSH alias from your client config (`~/.ssh/config`).

```bash
ssh-forward default my-dev
ssh-forward add 5173
ssh-forward status
# open http://127.0.0.1:5173
ssh-forward remove 5173
```

Or remember everything in one project directory on the host:

```bash
ssh-forward add --dir /home/dev/src/my-app
ssh-forward remove --dir /home/dev/src/my-app
```

`status` starts a per-user manager in the background. Later commands talk to that singleton over a user-only Unix socket. Use `ssh-forward manager serve` to run it in the foreground.

## How it compares

| | `ssh -L` | VS Code Ports | ssh-forward |
|---|---|---|---|
| Discovers remote listeners | no | yes (VS Code Server) | yes (agentless) |
| Policy | none | `remote.portsAttributes` | `add` / `remove` → `policies.jsonc` |
| SSH implementation | system OpenSSH | VS Code Remote-SSH | system OpenSSH |
| Credentials | your SSH config | your SSH config | your SSH config |
| Arbitrary tunnels | yes | limited | no (remote loopback ports only) |

## Commands

```text
ssh-forward add 5173                  # remember a remote port
ssh-forward add --dir /home/dev/app   # remember a Development Host directory
ssh-forward remove 5173
ssh-forward remove --dir /home/dev/app
ssh-forward default <alias>           # pin the default Development Host
ssh-forward status [--json]
ssh-forward watch [--json]            # stream snapshots (JSONL with --json)
ssh-forward policy list [--json]
ssh-forward host list [--json]        # hosts from the SSH client config
ssh-forward manager serve             # run the singleton in the foreground
```

`status` shows the host, connection, active forwards, and any new remote ports. Process details are in `--json`.

`--host` overrides the default host. `--ssh-config PATH` points at an explicit SSH client config. `SSH_FORWARD_CONFIG_DIR` overrides the product config directory.

## Agent skill

Install the usage skill for Cursor, Claude Code, Codex, and other agents:

```bash
npx skills add wangnan0916/ssh-forward
```

The skill lives in [`.agents/skills/ssh-forward/`](.agents/skills/ssh-forward/).

## Forwarding policies

`add` and `remove` write `policies.jsonc`. The manager hot-reloads it about every two seconds. You can still edit the file by hand for more specific matchers.

```jsonc
{
  "schema_version": 1,
  "policies": [
    {
      "id": "port-5173",
      "priority": 10,
      "action": "auto_forward",
      "conditions": [
        { "remote_ports": { "from": 5173, "to": 5173 } }
      ]
    },
    {
      "id": "dir-/home/dev/src/my-app",
      "priority": 10,
      "action": "auto_forward",
      "conditions": [
        { "working_directory_tree": "/home/dev/src/my-app" }
      ]
    }
  ]
}
```

Locations:

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/` (or `~/.config/ssh-forward/`)

## Status

Public **alpha** (`0.1.0-alpha.1`). The CLI covers discovery, remembered Auto-forward for ports and directories, reconnect, and JSON-RPC to a per-user manager.

Not in this release:

- loopback WebUI (`ssh-forward ui`)
- macOS menu-bar app and Dashboard
- comment-preserving policy writes
- login monitoring and idle manager exit
- HTTP/HTTPS “open in browser” actions
- Windows as a Local Machine
- more than one Development Host in a single manager process

## Security

Local Endpoints bind only to loopback. System `ssh` is launched by absolute path with an argument vector, never through a shell. The remote scanner is a fixed script; host aliases and process metadata never modify it. No SSH credential is stored.

See [SECURITY.md](SECURITY.md) to report a vulnerability and [docs/security/threat-model.md](docs/security/threat-model.md) for the model.

## Development

Fast tests do not require Docker:

```bash
cd cli
go test ./...
go test -race ./...
```

Real Linux/OpenSSH integration tests use only the disposable container harness and never resolve a configured Development Host:

```bash
./scripts/test-integration
```

The integration harness requires Docker Engine 28 or newer through a local Unix socket.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development path, test seams, and pull-request expectations.

## Design

- [Domain language](./CONTEXT.md)
- [Implementation sequence](./docs/design/implementation-sequence.md)
- [Core Manager Interface](./docs/design/core-interface.md)
- [JSON-RPC protocol](./docs/design/ipc-protocol.md)
- [CLI and state](./docs/product/cli-and-state.md)
- [ADRs](./docs/adr/)
- [Threat model](./docs/security/threat-model.md)

## License

[MIT](LICENSE)
