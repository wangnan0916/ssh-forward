# ssh-forward — automatic SSH port forwarding for remote development

Discover loopback services on a Linux SSH host and keep selected ports
available on the same port at `localhost` through system OpenSSH.

[![CI](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml/badge.svg)](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`ssh-forward` is a small SSH tunnel manager for remote development.
Authentication and connection options stay in OpenSSH and
`~/.ssh/config`.

## Why

Remote development servers often start HTTP applications on unpredictable or
short-lived ports. A manual `ssh -L` works, but you have to discover the port,
start the tunnel, and recreate it after the SSH connection changes.

`ssh-forward` shows remote loopback listeners, remembers only the ports you
choose, and keeps their local SSH port forwards running in the background. It
does not install a remote agent or store SSH credentials.

## Lightweight

Measured with `v0.1.0` on an Apple M1 Pro running macOS 26.6.2:

| Metric | Result |
| --- | ---: |
| Release archive, across four supported targets | 3.75–4.17 MB |
| Unpacked binary | 9.4–10.2 MB |
| Idle Manager with discovery only | 18.2 MiB total RSS |
| Idle Manager with one active forward | 23.2 MiB total RSS |
| Idle CPU, ten one-second samples | 0.0% |
| Warm `ssh-forward --version` startup | 7.4 ms average |

The runtime totals include the Manager and its system OpenSSH children: one
`ssh` process for discovery and one more per active forward. RSS can count
shared pages more than once. At the 256-port observation limit, a complete
Manager status snapshot takes about 28–30 µs with two allocations, and parsing
a complete scanner frame takes about 44–49 µs.

These numbers are a reproducible baseline rather than a performance guarantee.
Run the benchmarks with:

```bash
cd cli
go test -run '^$' -bench . -benchmem ./internal/core ./internal/openssh
```

## Install

The only runtime dependency is a system OpenSSH client. The local machine may
run macOS or Linux; the remote Development Host must run Linux.

With Homebrew:

```bash
brew install wangnan0916/ssh-forward/ssh-forward
```

Release archives for macOS and Linux on Apple Silicon/ARM64 and AMD64 are
available from [GitHub Releases](https://github.com/wangnan0916/ssh-forward/releases).

Or build from source with Go 1.26 or newer:

```bash
git clone https://github.com/wangnan0916/ssh-forward.git
cd ssh-forward/cli
go build -o ssh-forward ./cmd/ssh-forward
```

## Quick start

Use a literal `Host` alias from your SSH config:

```sshconfig
Host my-dev
  HostName dev.example.com
  User me
```

Then choose the host and remember the ports you want locally:

```bash
ssh-forward default my-dev
ssh-forward status              # see remote loopback listeners
ssh-forward add 5173            # keep remote 5173 on localhost:5173
ssh-forward status --watch      # follow changes
ssh-forward remove 5173
```

The first command that needs a connection automatically installs and starts a
user-scoped background manager. Later commands reuse it. After an upgrade, the
next command automatically replaces an older Manager.

## Commands

```text
ssh-forward add PORT
ssh-forward remove PORT
ssh-forward status [--json] [--watch]
ssh-forward host [--json]
ssh-forward default [ALIAS]
ssh-forward uninstall
```

Global options are `--host ALIAS` and `--ssh-config PATH`. Set
`SSH_FORWARD_CONFIG_DIR` to move product state.

## How it works

1. A fixed shell script runs through `ssh HOST sh -s` and reads
   `/proc/net/tcp` on the Linux host.
2. It reports at most 256 IPv4 loopback listeners. No remote agent is
   installed and no process metadata is collected.
3. For each remembered port, the Manager supervises one
   `ssh -N -L 127.0.0.1:PORT:127.0.0.1:PORT HOST` process. The local port stays
   available while the remote process restarts; individual connections fail
   until the remote listener returns.
4. HTTP over a user-only Unix socket lets later CLI calls read Manager status.
   `status --watch` polls that status.

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

Commands compare this file with the running Manager and restart it when the
selected host, remembered ports, or binary version changes. Runtime
observations and process IDs are not persisted.

Default directories:

- macOS: `~/Library/Application Support/ssh-forward/`
- Linux: `$XDG_CONFIG_HOME/ssh-forward/` or `~/.config/ssh-forward/`

## Upgrade and uninstall

Homebrew upgrades the binary normally:

```bash
brew upgrade ssh-forward
```

Run the product uninstall command before removing the binary. It removes the
background service but keeps `config.jsonc` for a later reinstall:

```bash
ssh-forward uninstall
brew uninstall ssh-forward
```

Delete the configuration directory separately if you also want to forget the
selected Host and ports.

## Current limits

- Linux Development Hosts only
- macOS and Linux local clients only
- one active SSH host per Manager
- IPv4 loopback TCP listeners only
- same local and remote port only

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
[SECURITY.md](SECURITY.md). Ask usage questions or report bugs through
[GitHub Issues](https://github.com/wangnan0916/ssh-forward/issues).

## License

[MIT](LICENSE)
