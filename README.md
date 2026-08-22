# ssh-forward

See the loopback TCP ports listening on one Linux SSH host and keep selected
ports available on the same port at `localhost`.

[![CI](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml/badge.svg)](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

The product deliberately does one job. It is not an SSH client, host-profile
manager, credential store, generic tunnel editor, or process-policy engine.
Authentication and connection options stay in system OpenSSH and
`~/.ssh/config`.

## Quick start

```bash
ssh-forward default my-dev
ssh-forward status              # see remote loopback listeners
ssh-forward add 5173            # keep remote 5173 on localhost:5173
ssh-forward remove 5173
```

`my-dev` is a literal `Host` alias from your SSH config. The first command that
needs a connection automatically installs and starts a user-scoped background
manager; later commands reuse it.

## Install

The only runtime dependency is a system OpenSSH client.

```bash
brew install --HEAD wangnan0916/ssh-forward/ssh-forward
```

Or build with Go 1.26 or newer:

```bash
cd cli
go build -o ssh-forward ./cmd/ssh-forward
```

## Commands

```text
ssh-forward add PORT
ssh-forward remove PORT
ssh-forward status [--json] [--watch]
ssh-forward watch [--json]
ssh-forward host [--json]
ssh-forward default [ALIAS]
ssh-forward manager stop|restart
```

Global options are `--host ALIAS` and `--ssh-config PATH`. Set
`SSH_FORWARD_CONFIG_DIR` to move product state.

## How it works

1. A fixed shell script runs through `ssh HOST sh -s` and reads
   `/proc/net/tcp` on the Linux host.
2. It reports at most 256 IPv4 loopback listeners. No remote agent is
   installed and no process metadata is collected.
3. For each remembered port that is currently listening, the manager
   supervises one `ssh -N -L 127.0.0.1:PORT:127.0.0.1:PORT HOST` process.
4. HTTP over a user-only Unix socket lets later CLI calls read manager status.
   `watch` polls that status; the server does not implement a streaming state
   platform.

The OS user service manager (launchd on macOS, the detected init system on
Linux) owns process startup, restart, and logs. Installation and startup happen
automatically when a command first needs the Manager.

There is no alternate local-port allocation. If the same local port is in use,
status reports the conflict and retries later.

## Configuration

All persistent intent is in one `config.jsonc`:

```jsonc
{
  "schema_version": 1,
  "default_host": "my-dev",
  "forwards": {
    "my-dev": [5173, 8080]
  }
}
```

The manager reloads this file while running. Runtime observations and process
IDs are not persisted.

Default directories:

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/` or `~/.config/ssh-forward/`

## Current limits

- Linux Development Hosts only
- one active SSH host per manager
- IPv4 loopback TCP listeners only
- same local and remote port only
- CLI and JSON status only; no desktop UI

## Development

```bash
cd cli
go test ./...
go test -race ./...
go vet ./...
```

The disposable Linux/OpenSSH integration test requires Docker:

```bash
./scripts/test-integration
```

See [ARCHITECTURE.md](ARCHITECTURE.md), [CONTRIBUTING.md](CONTRIBUTING.md), and
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
