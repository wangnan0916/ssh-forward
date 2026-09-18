# ssh-forward

Keep development services on Linux SSH hosts reachable from your local machine,
VMs, and containers. Import selected remote ports or services matching a working
directory; publish explicit local services back to remote loopback. System
OpenSSH owns authentication, keys, jump hosts, and transport. No remote agent is
installed and no credentials are stored by ssh-forward.

[![CI](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml/badge.svg)](https://github.com/wangnan0916/ssh-forward/actions/workflows/integration.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Install

The local machine must run macOS or Linux with OpenSSH; remote hosts must run
Linux. Install with Homebrew:

```sh
brew install wangnan0916/ssh-forward/ssh-forward
```

[Release archives](https://github.com/wangnan0916/ssh-forward/releases) support
ARM64 and AMD64 on both local platforms. To build, use Go 1.26.6 or newer:

```sh
git clone https://github.com/wangnan0916/ssh-forward.git
cd ssh-forward/cli
go build -o ssh-forward ./cmd/ssh-forward
```

## Use

Use an existing SSH alias, hostname, IP, or `user@host`:

```sh
ssh-forward host add my-dev
ssh-forward add --pwd '/home/me/Workspace/**'
ssh-forward add 5173
ssh-forward status --watch
```

Rules are global by default. Every remembered or discovered host participates
when its listener matches. `*` matches within a path component; `**` crosses
components. Quote globs to prevent local shell expansion. Automatic forwards
stop when the matching listener disappears; missing process metadata cannot
match a directory rule.

Use `--host` to scope a rule or retain a fixed mapping while the service is absent:

```sh
ssh-forward --host my-dev add 8443 --local 18443
ssh-forward --host my-dev status --json
ssh-forward --host my-dev remove 8443
ssh-forward remove --pwd '/home/me/Workspace/**'
```

Imports bind **local `0.0.0.0`** and target remote `127.0.0.1`. They are reachable
from other connected networks unless a firewall blocks them. From a VM, use the
host machine's address and reported local port. Implicit same-port imports try
up to 20 higher ports when busy; explicit `--local` mappings are strict. Status
shows actual ports; temporary fallback choices are never persisted.

Publishing always requires an explicit host:

```sh
ssh-forward --host my-dev publish 9222
ssh-forward --host my-dev publish 9222 --remote 19222
ssh-forward --host my-dev unpublish 9222
```

Both publication endpoints are fixed to IPv4 loopback. The remote port is strict:
conflicts or sshd restrictions appear as failures and retry later. The actual
remote bind is verified; wildcard overrides from `GatewayPorts yes` are rejected
and canceled. Use `GatewayPorts no` or `clientspecified`. Published local service
ports are reserved across all imports, including while the local service is down.

For Chrome DevTools, point the remote MCP process at the published endpoint:
`--browser-url=http://127.0.0.1:9222`. See [Security](SECURITY.md) for network
exposure, authentication, and browser-profile precautions.

## Hosts and recovery

```sh
ssh-forward host                         # list remembered/discovered IDs
ssh-forward host add staging --target me@192.168.1.20 --port 2222
ssh-forward host discover                # scan local SSH sessions now
ssh-forward host ignore staging
ssh-forward host enable staging
ssh-forward host aliases                 # list SSH-config candidates only
```

The Manager scans same-user SSH processes at startup and every five seconds,
reading exact native argv and excluding its own process tree. It remembers new
targets in a locked `discovered-hosts.json` registry; closing the original SSH
session does not forget them. Supported options include port, user, absolute
identity/config paths, jump hosts, and selected `-o` settings. Different settings
receive distinct IDs. Unsupported argv options are dropped; the destination still
auto-monitors through OpenSSH defaults and SSH config. Use `host add` only when
you need explicit overrides (`--target`, `--port`, `--user`, `--identity`,
`--jump`, or `--ssh-config`).

Embedded SSH clients, inaccessible argv, and sessions between scans may be
missed until the next successful scan. Merely listing an alias in SSH config
does not connect it until a matching session is discovered or you add the host.
Ignoring a destination also suppresses its discovered variants, across restarts.
Host edits reload within five seconds; rule edits reload immediately. Discovered
targets use noninteractive SSH authentication.

One user service owns an independent SSH master per host. Encrypted keepalives
run after five idle seconds and disconnect after three unanswered probes.
Discovery and both forwarding directions reconnect automatically after a server
reboot or silent network loss, once SSH becomes reachable again. Connection and
control operations have bounded timeouts. A failed host does not stop others.

## Status and diagnostics

```sh
ssh-forward status [--json] [--watch]
ssh-forward --host my-dev status
ssh-forward doctor --host my-dev [--json]
ssh-forward COMMAND --help
```

Status includes every monitored host, including offline hosts and candidates
needing settings. JSON emits an array by default and one object with `--host`.
Watch appends changed snapshots. Doctor checks configuration, SSH, the existing
Manager, forward failures, and a real remote scan without repairing anything.

The remote scanner reads Linux procfs and reports at most 256 listeners reachable
at IPv4 loopback. Same-user IPv4 and dual-stack wildcard listeners are included;
IPv6-only listeners are excluded. Loopback listeners that speak SSH (or whose
executable is `sshd`) are omitted. Executable names and working directories are best
effort; when Docker is reachable for the scanning user, published container
ports can show the Compose service name and project directory so working-
directory rules may select them. UDP, Unix sockets, arbitrary publication bind
addresses, and dynamic remote ports are not supported.

## Configuration and lifecycle

`config.jsonc` stores hosts, ignored hosts, global rules, and optional scoped rules:

```jsonc
{
  "schema_version": 6,
  "hosts": {"dev": {"target": "dev"}},
  "global_forwards": [{"remote_port": 5173}],
  "global_working_directory_rules": ["/workspace/**"],
  "remembered_forwards": {"dev": [{"remote_port": 8443, "local_port": 18443}]},
  "published_forwards": {"dev": [{"local_port": 9222}]},
  "ignored_hosts": []
}
```

Schemas 1–5 upgrade on the next write without broadening scoped rules. Legacy
`default_host` becomes a remembered host; there is no default-host selection or
interactive picker. Explicit mappings override global port rules for that host.

Default state directories are `~/Library/Application Support/ssh-forward/` on
macOS and `$XDG_CONFIG_HOME/ssh-forward/` (otherwise `~/.config/ssh-forward/`) on
Linux. Override with `SSH_FORWARD_CONFIG_DIR`. A Manager shares one SSH config
file unless a host supplies its own connection settings.

Commands needing a connection install/start the user service automatically.
Rule changes are saved before connecting, so intent survives a temporary service
failure. The next connection after an upgrade replaces an incompatible Manager.
Use `brew upgrade ssh-forward` normally. Before removing the binary:

```sh
ssh-forward uninstall
brew uninstall ssh-forward
```

Uninstall keeps configuration; delete its directory separately to forget intent.

## Development

```sh
./scripts/dev status          # latest CLI against installed Manager
./scripts/dev --full status   # temporary dev Manager; restores installed service
./scripts/check               # unit, race, vet, formatting, modules, benchmarks
./scripts/test-integration    # disposable local Docker/OpenSSH fixture
```

Dev builds live in `.tmp/dev/ssh-forward`; `SSH_FORWARD_DEV_BASELINE` selects the
installed baseline. See [Contributing](CONTRIBUTING.md) for checks and releases,
[Architecture](ARCHITECTURE.md) for ownership and library choices, and
[GitHub Issues](https://github.com/wangnan0916/ssh-forward/issues) for support.
