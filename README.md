# ssh-forward — automatic SSH port forwarding for remote development

Discover services reachable through IPv4 loopback on a Linux SSH host and keep
selected ports—or ports whose process working directory matches a configured
glob—available on all local IPv4 interfaces through system OpenSSH. Explicit
local services can also be published on the Development Host's loopback.
Remembered forwards may use a different preferred local port and choose
whether to fall back when it is busy. Automatic forwards always allow
temporary fallback.

[![CI](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml/badge.svg)](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`ssh-forward` is a small SSH tunnel manager for remote development.
Authentication and connection options stay in OpenSSH and
`~/.ssh/config`.

## Why

Remote development servers often start HTTP applications on unpredictable or
short-lived ports. A manual `ssh -L` works, but you have to discover the port,
start the tunnel, and recreate it after the SSH connection changes.

`ssh-forward` shows remote listeners reachable at `127.0.0.1`, remembers the
ports and working-directory globs you choose, and keeps the required local SSH
port forwards listening at `0.0.0.0` in the background. Imported services are
therefore reachable from local virtual machines such as UTM and from other
networks that can reach the local machine. It can also publish an explicit local
port back to remote `127.0.0.1` without requiring an inbound connection to the
local machine. It does not install a remote agent or store SSH credentials.

## Compared with `ssh -L`, autossh, and editor port forwarding

Manual `ssh -L` is a good fit for one known, stable port mapping. autossh can
monitor and restart a predefined SSH tunnel. Editor-integrated port forwarding
is convenient when a remote development session owns the workflow.

`ssh-forward` is for development services on the same Linux SSH host that need
to remain available across terminals, editors, and local virtual machines. It
automatically discovers loopback dev-server ports and keeps remembered or
working-directory matched forwards active in a user background process, and
delegates transport, authentication, jump hosts, and connection options to
system OpenSSH.

## Automatically forward project services

Forward short-lived development servers whose working directories are anywhere
under a remote workspace, without discovering and adding each port:

```bash
ssh-forward add --pwd '/home/me/Workspace/**'
```

When a matching remote process starts listening, its port becomes available on
the same port at local `0.0.0.0`, or the next available port if that port is
busy.
When the listener stops, the automatic SSH forward disappears. This works well
for development servers, preview tools, notebooks, and OAuth callback servers
that use temporary ports.

Imported ports accept connections on every local IPv4 interface. From a UTM
guest, connect to the macOS host address and the reported port, not guest
`127.0.0.1`. Other machines on a reachable LAN may also connect, so expose only
trusted development services and use application authentication or a host
firewall when needed.

## Publish a local service to the Development Host

Make a service on the local machine available only through remote loopback:

```bash
ssh-forward --host my-dev publish 9222                 # local 9222 -> remote 127.0.0.1:9222
ssh-forward --host my-dev publish 9222 --remote 19222  # local 9222 -> remote 127.0.0.1:19222
ssh-forward status --watch
ssh-forward --host my-dev unpublish 9222
```

The remote port is stable and strict: if it is occupied or the SSH server
rejects remote forwarding, status reports a failure and the Manager retries.
Both endpoints are fixed to IPv4 loopback; this command cannot request a
public remote bind.

SSH forwarding adds no application authentication; the Local Service's own
authentication and trust model still apply.

Chrome DevTools MCP is one example: publish Chrome's local debugging port, then
configure the MCP process on the Development Host with
`--browser-url=http://127.0.0.1:9222`. DevTools access can control the browser
profile, so use a dedicated profile without sensitive browsing, publish it only
to a trusted single-user Development Host, and unpublish the port when it is no
longer needed.

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

The v0.1.0 runtime totals include the Manager and the system OpenSSH process
layout used by that release. Current Managers use one product-owned OpenSSH
master connection per host and add or cancel forwards through its control socket, so
the number of SSH transports no longer grows with the number of ports. RSS can
count shared pages more than once. At the 256-port observation limit, a
complete Manager status snapshot takes about 28–30 µs with two allocations,
and parsing a complete scanner frame takes about 44–49 µs.

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

Remember a host (or let the Manager discover your active SSH sessions), then add global rules:

```bash
ssh-forward host add my-dev
ssh-forward status              # see listeners and forwards across all hosts
ssh-forward add 5173            # forward matching listeners on every host
ssh-forward add 8443 --local 18443  # require remote 8443 on 0.0.0.0:18443
ssh-forward add --pwd '/home/me/Workspace/**'  # forward matching live services
ssh-forward --host my-dev publish 9222  # publish only to this host
ssh-forward status --watch      # follow changes
ssh-forward remove 5173
ssh-forward remove --pwd '/home/me/Workspace/**'
ssh-forward --host my-dev unpublish 9222
```

For a connection that works with OpenSSH's defaults, `--host` also accepts a
hostname, IP address, or `user@host` without adding an SSH config alias:

```bash
ssh-forward --host 192.168.1.20 status
ssh-forward --host ubuntu@192.168.1.20 publish 9222
```

`--host` names the SSH target; it does not accept a complete `ssh` command or
forward OpenSSH flags such as `-p`, `-i`, `-J`, or `-o`. Keep custom ports,
identities and jump hosts in SSH config, or use `host add NAME` with explicit
connection options. `--host` can select a remembered host ID.

The first command that needs a connection automatically installs and starts a
user-scoped background manager. Later commands reuse it. After an upgrade, the
next command automatically replaces an older Manager.

## Commands

```text
ssh-forward add PORT [--local PORT]
ssh-forward add --pwd GLOB
ssh-forward remove PORT
ssh-forward remove --pwd GLOB
ssh-forward --host TARGET publish LOCAL [--remote REMOTE]
ssh-forward --host TARGET unpublish LOCAL
ssh-forward status [--json] [--watch]
ssh-forward doctor --host TARGET [--json]
ssh-forward host [--json]
ssh-forward host add NAME [--target TARGET] [--port PORT] [--user USER]
ssh-forward host discover
ssh-forward host ignore HOST
ssh-forward host enable HOST
ssh-forward host aliases
ssh-forward uninstall
```

Global options are `--host TARGET` and `--ssh-config PATH`. Set
`SSH_FORWARD_CONFIG_DIR` to move product state.

## How it works

1. A fixed shell script runs through `ssh HOST sh -s` and reads Linux procfs
   listener state.
2. It reports at most 256 TCP listeners reachable at `127.0.0.1`, including
   same-user IPv4 wildcard listeners and dual-stack IPv6 wildcard listeners.
   Restricting wildcard discovery to the SSH user avoids listing system-wide
   services such as the SSH daemon itself. IPv6-only listeners stay hidden
   because the forwarding target cannot reach them. Executable names and
   working directories are collected on a best-effort basis when `ss` and the
   relevant procfs links are available. No remote agent is installed.
3. The Manager owns one product-private OpenSSH master connection per host
   and uses OpenSSH control commands to add and cancel each desired remote-to-local or
   local-to-remote forward. Remote-to-local forwards bind `0.0.0.0`, including
   Remembered and Automatic Forwards, so local virtual machines can reach them.
   The local port stays available while the remote process restarts; individual
   connections fail until the remote listener returns. Stopping one Forward
   does not disturb the shared connection or other ports.
4. Absolute working-directory globs create Automatic Forwards for matching
   Remote Listeners. `*` matches within one path segment and `**` crosses path
   segments. When a listener disappears or stops matching, its Automatic
   Forward stops. Quote globs so the local shell does not expand them.
5. Remembered Forwards created without `--local` and Automatic Forwards try up
   to 20 higher local ports when the preferred port is busy. The actual port
   appears in status but is temporary and is never written to configuration.
   Explicit `--local` mappings are strict by default.
6. Published Forwards use strict OpenSSH remote forwarding from one explicit
   local loopback service to one explicit Development Host loopback port. They
   persist across Manager and SSH reconnects and do not participate in local
   listener discovery. Before reporting one active, the Adapter verifies its
   actual remote procfs socket and cancels wildcard binds forced by
   `GatewayPorts yes`. If cancellation of an installed forward fails, it closes
   the product-owned SSH master to guarantee that the listener is removed.
7. HTTP over a user-only Unix socket lets later CLI calls read Manager status.
   `status --watch` polls status for all hosts (or the explicit `--host`).
8. SSH masters send encrypted keepalives after 5 seconds without incoming
   traffic and disconnect after 3 unanswered probes (roughly 15 seconds).
   Discovery and both forwarding directions then retry automatically, so a
   server reboot or silent network loss does not require restarting the
   Manager. Recovery completes when the server becomes reachable again.
   Connection attempts and control commands have bounded timeouts.

The OS user service manager (launchd on macOS, the detected init system on
Linux) owns process startup, restart, and logs. Installation and startup happen
automatically when a command first needs the Manager.

Run `ssh-forward doctor` for a read-only check of config files, OpenSSH, Host
selection, Manager health, Forward failures, and a real remote listener scan.
It does not install, restart, or change the Manager. Use `--json` for
automation.

Use `add REMOTE --local LOCAL` when a stable local address is required. The
command creates a strict mapping: if another process owns that port, status
reports the conflict and retries later. Remembered preferred ports for one
Host must be distinct.

## Configuration

`config.jsonc` contains an explicit host list, global rules, optional host-scoped
rules, and ignored hosts. No default host is required. `add` and `remove` apply
globally unless `--host` is supplied; `publish` and `unpublish` always require
an explicit `--host`.

```bash
ssh-forward host add dev
ssh-forward host add staging --target me@192.168.1.20 --port 2222
ssh-forward add --pwd '/workspace/**'
ssh-forward add 5173
ssh-forward status --watch
ssh-forward host ignore staging
ssh-forward host enable staging
```

Global directory rules match each observed listener's working directory. Global
port rules forward only while that port is observed on the remote host. If
several hosts match, all participate. Host-scoped `add PORT --host HOST` retains
the fixed remembered mapping behavior, including while the remote service is
absent. Scoped fixed mappings take precedence over global port rules.

The Manager discovers SSH processes owned by the current OS user at startup
and every five seconds. macOS uses native process arguments; Linux reads procfs
argv. It excludes its own process tree and control commands. It remembers
plain SSH destinations and supported connection options (port, user, absolute
identity/config paths, jump host, and selected `-o` settings) in the separate
`discovered-hosts.json` registry. Different connection settings receive distinct
host IDs. Closing the original SSH session does not forget its target. Use
`host discover` to scan immediately and `host` to inspect IDs and ignored state.

Unsupported connection parameters appear as candidates needing connection
settings, without being connected automatically. Use `host add NAME --target
DESTINATION` with `--port`, `--user`, `--identity`, `--jump`, and `--ssh-config` as
needed. SSH implementations embedded inside applications, inaccessible argv,
short-lived sessions between scans, and ambiguous/unrecognized invocations may
not be discoverable. Relative key/config paths need manual completion.
`host aliases` lists SSH-config candidates; aliases alone are not all connected.

`host ignore HOST` persists across scans and restarts and stops that host within
five seconds. Ignoring a destination also suppresses discovered variants of
that destination. `host enable HOST` re-enables it. A running Manager reloads
config changes every five seconds; command mutations also apply immediately.
Discovered hosts use noninteractive authentication through the user's existing
SSH configuration/agent; passwords and remote commands are never saved.

```jsonc
{
  "schema_version": 6,
  "hosts": {
    "dev": {"target": "dev"},
    "staging": {"target": "me@192.168.1.20", "arguments": ["-p", "2222"]}
  },
  "global_forwards": [
    {"remote_port": 5173, "local_port": 5173, "allow_fallback": true}
  ],
  "global_working_directory_rules": ["/workspace/**"],
  "ignored_hosts": [],
  "remembered_forwards": {
    "dev": [{"remote_port": 8443, "local_port": 18443}]
  },
  "published_forwards": {
    "dev": [{"local_port": 9222, "remote_port": 9222}]
  }
}
```

`status` and `status --watch` show every monitored host, including disconnected
hosts and candidates requiring settings. `status --json` emits an array;
`status --host dev --json` emits one object. Local ports are shared across hosts:
automatic fallback chooses another local port when needed, while an explicit
`--local` is strict. Status shows the source host and actual local port.
Published local service ports are reserved across all hosts.

Schemas 1–5 remain readable and upgrade to schema 6 on the next write. Existing
remembered mappings, published mappings, and directory rules stay scoped to
their original host; migration never broadens them globally. Legacy
`default_host` migrates to a remembered host and is removed on the next write.
The old `default` command and interactive host picker are removed. Listener observations, actual fallback ports,
and process IDs remain volatile.

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
remembered hosts and ports.

## Current limits

- Linux Development Hosts only
- macOS and Linux local clients only
- one shared SSH configuration file per Manager; multiple hosts run concurrently
- TCP listeners reachable through remote `127.0.0.1`; IPv6-only listeners are
  excluded
- Imported ports bind local `0.0.0.0` and may be reachable from virtual
  machines, containers, and other connected networks
- Published services use TCP and IPv4 loopback at both ends; remote wildcard
  binding, UDP, Unix sockets, and dynamic remote ports are not supported.
  Development Host sshd must use `GatewayPorts no` or `clientspecified`;
  wildcard binds forced by `GatewayPorts yes` are rejected and canceled
- Automatic Forwards require best-effort process working-directory metadata;
  listeners without that metadata cannot match a rule

## Development

For CLI-only changes, build and run the latest code against the installed
Manager without replacing the background service:

```bash
./scripts/dev status
```

Use full mode when Manager behavior also changed. It temporarily runs the
development Manager and restores the installed one when the command exits:

```bash
./scripts/dev --full status
```

Both modes build an ignored binary at `.tmp/dev/ssh-forward`. By default the
baseline binary is the `ssh-forward` found on `PATH`; set
`SSH_FORWARD_DEV_BASELINE` to select another installed binary.

Run the complete local check suite with:

```bash
./scripts/check
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
