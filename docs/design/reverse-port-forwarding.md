# Reverse Port Forwarding

Status: Implemented

## Summary

Add a host-scoped **Published Forward** that makes one explicit local IPv4
loopback TCP service available on the selected Development Host's IPv4
loopback. The local Manager remains the only resident process and the local
machine remains the SSH client. The existing product-owned OpenSSH master
gains dynamic `-R` requests alongside its current dynamic `-L` requests.

The first release is deliberately narrow:

- explicit persistent ports only;
- `127.0.0.1` at both ends;
- one selected Development Host;
- a strict, stable remote port with no fallback;
- no local listener or working-directory discovery;
- no remote wildcard binding and no public-network exposure;
- no MCP transport proxying.

Chrome DevTools MCP is one example: publish Chrome's local DevTools port while
the MCP process remains on the Development Host.

## Motivation

The current product imports a Development Host service onto the local machine:

```text
local 127.0.0.1:LOCAL ── ssh -L ──> Development Host 127.0.0.1:REMOTE
```

Remote development also needs the inverse operation. An agent or tool running
on the Development Host may need to use a service that can only run on the
developer's machine, such as Chrome DevTools:

```text
Development Host 127.0.0.1:REMOTE ── ssh -R ──> local 127.0.0.1:LOCAL
```

OpenSSH remote forwarding does not require the Development Host to initiate an
SSH connection back to the local machine. The existing local SSH client opens
the remote loopback listener through the already authenticated connection.

## Previous architecture

Before this feature, the [product contract](../../ARCHITECTURE.md) was
specifically remote-to-local. That implementation reflected the contract at
every layer:

- [`RememberedForward`](../../cli/internal/core/status.go) stores a Remote Port,
  Preferred Local Port, and local fallback policy.
- [`manager`](../../cli/internal/core/manager.go) keys workers and status by
  Remote Port and reconciles remembered and directory-selected remote
  listeners.
- [`Backend.Forward`](../../cli/internal/core/status.go) reports only the actual
  Local Port through its ready callback.
- [`openssh.Adapter`](../../cli/internal/openssh/adapter.go) formats
  `127.0.0.1:LOCAL:127.0.0.1:REMOTE`, probes the local listening port, and
  retries higher local ports when allowed.
- [`runControl`](../../cli/internal/openssh/master.go) hard-codes `-L` for both
  `-O forward` and `-O cancel`.
- [`configFile`](../../cli/internal/app/config.go), Manager IPC, JSON status,
  human status, doctor output, and the CLI all assume that Remote Port is the
  identity of a Forward.

The OpenSSH transport is already the right deep module. It owns one private
master per Host, dynamic forwarding, bounded stderr classification, and
cleanup. Reverse forwarding should deepen this module instead of creating a
second transport or Manager.

## Goals

1. A user can persistently publish `127.0.0.1:LOCAL` as
   `127.0.0.1:REMOTE` on the selected Development Host.
2. The Published Forward automatically returns after SSH reconnects, a Manager
   restart, or a local machine wake.
3. Adding, changing, or removing one Published Forward does not disturb
   listener Discovery or any other Forward.
4. Existing configuration, commands, human output, and compact JSON output
   retain their meaning.
5. A remote bind conflict or server-side forwarding restriction has a bounded,
   actionable diagnostic.
6. The remote listener never becomes reachable outside Development Host
   loopback through a product option.

## Non-goals

- Discovering local listeners automatically.
- Matching local processes by working directory.
- Publishing UDP, Unix sockets, IPv6-only listeners, or arbitrary hosts.
- Allowing a remote bind address other than `127.0.0.1`.
- Providing access to a second Development Host from one Manager.
- Forwarding `stdio` MCP traffic directly.
- Guaranteeing that the local target application is healthy. As with an
  existing Remembered Forward whose remote application is restarting, active
  means the SSH listening endpoint exists.

## Domain language

The implementation adopts these terms in `CONTEXT.md`:

- **Local Service**: a TCP endpoint reachable at `127.0.0.1:PORT` on the local
  machine. It is an explicitly configured target, not a discovered listener.
- **Published Port**: the TCP port bound at `127.0.0.1:PORT` on the Development
  Host by a Published Forward.
- **Published Forward**: persistent intent to map one Local Service to one
  Published Port through OpenSSH remote forwarding.
- **Forward Direction**: either `remote_to_local` for the current behavior or
  `local_to_remote` for a Published Forward.

Avoid calling the new concept a "reverse connection": the SSH connection is
not reversed. Avoid calling the local endpoint a Local Listener because this
release does not observe or own that socket.

Existing terms remain unchanged. A Remembered Forward and an Automatic
Forward are always `remote_to_local`; a Published Forward is always
`local_to_remote`.

## User interface

### Commands

Add two daily commands rather than adding a direction flag to `add` and
`remove`:

```text
ssh-forward publish LOCAL [--remote REMOTE] [--json]
ssh-forward unpublish LOCAL [--json]
```

Examples:

```bash
# local 9222 becomes Development Host 127.0.0.1:9222
ssh-forward publish 9222

# local 9222 becomes Development Host 127.0.0.1:19222
ssh-forward publish 9222 --remote 19222

ssh-forward unpublish 9222
```

`LOCAL` is the identity used by `unpublish`. One Local Service may have at most
one Published Forward per Host in the first release. A Published Port must also
be unique per Host. Re-running an equivalent `publish` is idempotent. Publishing
the same Local Service with a different remote port updates the existing intent
and restarts only that worker. Unpublishing a Local Service that is not
configured returns an error, matching the current `remove` behavior.

The remote port defaults to the local port and is strict. A conflict fails
instead of silently choosing another remote port because remote clients need a
stable configured address. Dynamic remote ports and fallback can be considered
later as an explicit feature.

The command returns after config persistence and `UpdateIntent` succeed; it
does not claim that the SSH listener is already active. Human output reports
the publishing intent, and `status` / `status --watch` remains the live health
interface. Stable mutation JSON is:

```json
{"added":true,"host":"my-dev","local_port":9222,"remote_port":9222}
```

`unpublish --json` uses `removed` instead of `added`.

### Human output

Keep imported and published traffic visually separate:

```text
Host  my-dev    Discovery  active

FORWARDS
REMOTE  TARGET            KIND        APP   WORKING DIRECTORY
  5173  127.0.0.1:5173    remembered  node  /home/me/app

PUBLISHED
LOCAL  REMOTE TARGET      KIND
 9222  127.0.0.1:9222     published
```

Use `PUBLISHING` for starting state and `PUBLISH NEEDS ATTENTION` for failures.
Do not add a terminal hyperlink for the remote endpoint because it is not
reachable from the local terminal.

Discovery continues to describe only Development Host application listeners.
A Published Port created by SSH must not appear as an Available Port or acquire
remote process metadata in the UI. The existing scanner may observe the SSH
listener. Core excludes desired Published Ports from Automatic Forward
selection and excludes active Published Ports from the listener status exposed
to renderers.

### JSON output

Preserve the current compact shape for unchanged same-port remote-to-local
Forwards. Published Forwards always use an explicit shape:

```json
{
  "direction": "local_to_remote",
  "local_port": 9222,
  "preferred_remote_port": 9222,
  "remote_port": 9222,
  "state": "active",
  "kind": "published"
}
```

`preferred_remote_port` and `remote_port` are both included even though they
are equal in the first release. This makes a later explicitly requested dynamic
or fallback mode additive instead of changing field meaning.

Do not return `core.ForwardStatus` directly from `forwardJSONStatus` for either
direction. Introduce direction-specific output DTOs so existing compact and
expanded remote-to-local JSON remain byte-for-byte stable while Published
Forwards use the new explicit shape. `kind` is derived as `published` from the
direction; it is not persistent intent.

## Persistent configuration

Bump `configSchemaVersion` from 4 to 5 and add a separate host-scoped map:

```jsonc
{
  "schema_version": 5,
  "default_host": "my-dev",
  "remembered_forwards": {
    "my-dev": [
      {"remote_port": 5173, "local_port": 5173, "allow_fallback": true}
    ]
  },
  "published_forwards": {
    "my-dev": [
      {"local_port": 9222, "remote_port": 9222}
    ]
  },
  "working_directory_rules": {
    "my-dev": ["/home/me/Workspace/**"]
  }
}
```

Use a separate `PublishedForward` type:

```go
type PublishedForward struct {
	LocalPort  uint16 `json:"local_port"`
	RemotePort uint16 `json:"remote_port"`
}
```

Its `WithDefaults` method sets an omitted Remote Port to Local Port. Config
normalization enforces:

- both effective ports are between 1 and 65535;
- Local Port is unique within a Host and is the sorted identity;
- Remote Port is unique within a Host;
- empty Host aliases remain invalid;
- a strict Remembered Forward cannot prefer a Local Port reserved by a
  Published Forward;
- other cross-direction numeric reuse remains valid because the two directions
  bind on different machines. A fallback-enabled Remembered or Automatic
  Forward may move around a reserved Local Port.

Schemas 1 through 4 load with an empty `PublishedForwards` map. The next write
upgrades the file to schema 5. No existing entry changes meaning. Saving removes
empty maps in the same way as current remembered and rule maps.

`HostIntent` returns cloned Remembered Forwards, Published Forwards, and Working
Directory Rules. `SetPublishedForward` and `RemovePublishedForward` follow the
existing idempotent config mutation pattern. Both Published and Remembered
mutations run the cross-collection reservation validation so mutation order
does not change the result.

## Core model

### Public types

Extend the current types without exposing OpenSSH flags:

```go
type ForwardDirection string

const (
	RemoteToLocal ForwardDirection = "remote_to_local"
	LocalToRemote ForwardDirection = "local_to_remote"
)

type ForwardTarget struct {
	Direction  ForwardDirection
	LocalPort  uint16
	RemotePort uint16
}

type ForwardingIntent struct {
	RememberedForwards    []RememberedForward
	PublishedForwards     []PublishedForward
	WorkingDirectoryRules []string
}

type ForwardStatus struct {
	Direction           ForwardDirection `json:"direction"`
	RemotePort          uint16           `json:"remote_port"`
	PreferredRemotePort uint16           `json:"preferred_remote_port,omitempty"`
	PreferredLocalPort  uint16           `json:"preferred_local_port,omitempty"`
	LocalPort           uint16           `json:"local_port"`
	State               ForwardState     `json:"state"`
	Diagnostic          string           `json:"diagnostic,omitempty"`
	Automatic           bool             `json:"automatic,omitempty"`
	AllowFallback       bool             `json:"allow_fallback,omitempty"`
}
```

Core-generated targets and statuses set Direction explicitly. Existing status
fixtures with a zero Direction continue to render as `RemoteToLocal` for
compatibility. Persisted and public JSON never rely on the zero value for a
Published Forward.

Change the true-external seam to:

```go
type Backend interface {
	Observe(context.Context, HostAlias, func([]Listener)) error
	Forward(context.Context, HostAlias, ForwardTarget, func()) error
	Close(context.Context) error
}
```

`ForwardTarget` is one exact pair of endpoints. The ready callback only marks
that exact target active. Direction-specific flag formatting, conflict
classification, probing, and cleanup stay inside the OpenSSH Adapter.

Move temporary local fallback out of the Adapter and into Core. Fallback is a
product reconciliation policy expressed by `RememberedForward.AllowFallback`,
not an OpenSSH transport mechanism. This also lets Core skip Local Ports
reserved by Published Forwards without leaking a reservation list through the
Backend interface.

### Private identity and desired state

The current `map[uint16]` worker and status keys cannot distinguish a
remote-to-local Forward for Remote Port 9222 from a local-to-remote Forward for
Local Port 9222. Replace them with a private composite key:

```go
type forwardKey struct {
	direction   ForwardDirection
	servicePort uint16
}
```

`servicePort` is the configured application endpoint: Remote Port for existing
Remembered and Automatic Forwards, Local Port for Published Forwards. Keeping
this type private prevents its asymmetric identity rule from leaking into the
Manager interface.

Build one private desired-state map on each reconciliation:

```go
type desiredForward struct {
	preferred     ForwardTarget
	automatic     bool
	allowFallback bool
}
```

Its key is derived from the preferred target, avoiding a second copy of the
direction and service-port identity.

This replaces direction-specific loops with one reconciliation algorithm:

1. Build desired remembered remote-to-local targets.
2. Add every published local-to-remote target.
3. Add matching automatic remote-to-local targets only when no remembered
   target owns the same key or Published Port suppresses it.
4. Build a reservation set from every Published Forward's Local Port.
5. Cancel workers absent from desired state, whose preferred target changed,
   or whose current local binding became reserved.
6. Start missing workers.

One worker still owns one logical Forward and retries it until its intent is
removed or the Manager closes. Changing one target cancels and replaces only
that worker. Discovery failure does not cancel Remembered or Published
Forwards.

For a fallback-enabled remote-to-local worker, Core tries the Preferred Local
Port and at most the next 20 ports, preserving the current policy. It skips
reserved Published Local Ports without calling the Backend and advances after
a `local_port_conflict` result. A new retry cycle starts again at the preferred
port. Published Forwards are strict and never enter this allocator.

If malformed intent reaches the Manager directly over IPC with a strict
Remembered Forward targeting a reserved Local Port, keep both intent rows but
report that Remembered Forward as failed with `local_port_reserved`. Valid
schema-5 config rejects the combination before it reaches IPC.

`ForwardStatus` gains Direction and Preferred Remote Port. Construct status
from the worker's desired record rather than looking it up again by Remote Port;
this avoids direction collisions and keeps source metadata stable during
cancellation. The worker's exact candidate supplies the actual Local and
Remote Ports.

## OpenSSH Adapter

### Dynamic requests

Replace `runControl(..., forward string)` with a private request that carries
the OpenSSH flag and exact cancellation specification:

```go
type controlForward struct {
	flag string // internally only "-L" or "-R"
	spec string
}
```

Formatting remains private to the Adapter:

```text
remote_to_local:
  -L 127.0.0.1:LOCAL:127.0.0.1:REMOTE

local_to_remote:
  -R 127.0.0.1:REMOTE:127.0.0.1:LOCAL
```

The same `controlForward` value must be used for `-O forward` and deferred
`-O cancel`. All values remain an argument vector; no shell interpolation is
introduced.

The master is still started once with `-M -N -T`, and each Forward remains a
logical worker over that transport. Do not create one SSH process per Published
Forward.

OpenSSH's `ClearAllForwardings=yes` also clears forwarding flags supplied on
the same command line, so it cannot be added blindly to the current
`ssh -O forward -L ...` invocation. Isolate configured and product-owned
forwarding in three parts:

1. Start the product-owned master with the selected user config and
   `-o ClearAllForwardings=yes`. Authentication, HostName, User, ProxyJump,
   identity, and connection options still apply, but configured
   `LocalForward`, `RemoteForward`, and `DynamicForward` entries do not become
   invisible Manager state.
2. Replace the `%C`-dependent control-socket template with an Adapter-owned,
   bounded filename relative to the private control directory. Invoke later
   `-O check`, `forward`, `cancel`, and `exit` clients with `-F /dev/null`, that
   explicit socket path, and only the product's `-L` or `-R` request. The short
   relative path stays below macOS Unix-socket limits. A mux control client uses
   the existing authenticated master and does not need to authenticate again.
3. During upgrade, use the selected SSH config to address the legacy
   `master-%C` socket in the same private directory. Request exit and wait
   boundedly for a responding legacy master to disappear before starting the
   alias-hash replacement, preventing orphaned `-L` listeners from blocking
   reconciliation.

The real-OpenSSH integration suite covers this isolated mux configuration. A
regression test specifically verifies that private master commands reuse a Host
alias configured only in the selected user config. It also starts a real
legacy `%C` master with an active forward and verifies that the replacement
Manager reclaims the same port during upgrade.

### Readiness semantics

For remote-to-local, retain the current local TCP probe because the Adapter can
directly verify the local listening socket and use it to distinguish a local
bind conflict. Remove the Adapter's 20-port loop: it receives one exact target
from Core and either activates it or returns a bounded error.

For local-to-remote, `ssh -O forward -R ...` returning success means sshd
accepted and installed a remote listener, but `GatewayPorts yes` may override
the requested address with wildcard. Before readiness, run a bounded command
through the same master and inspect the actual Linux procfs socket. Accept only
the requested IPv4 loopback bind; cancel wildcard listeners and fail closed
when the bind cannot be verified. Do not require the Local Service to be
listening: keeping the Published Port stable while the local application
restarts is symmetrical with the current behavior for a restarting remote
application.

The ready callback is therefore:

```text
-L: control request succeeds -> local probe succeeds -> ready()
-R: control request succeeds -> remote bind is loopback -> ready()
```

A connection through an active Published Forward may still fail while the
Local Service is absent. That is target availability, not Forward health.

### Conflicts and restrictions

Remote forwarding may fail because:

- Published Port is already bound;
- sshd disables remote forwarding through `AllowTcpForwarding`;
- sshd restricts the requested endpoint through `PermitListen`;
- the shared transport is unavailable.

Classify a precise diagnostic when stderr makes the cause reliable. When the
multiplex control client supplies only a generic failure and the master remains
healthy, report `remote_port_unavailable`; do not guess between conflict and
server policy.

Add bounded diagnostics:

| Diagnostic | Meaning | Remediation |
| --- | --- | --- |
| `remote_port_unavailable` | sshd did not install the requested remote loopback listener | Check whether the port is in use and review `AllowTcpForwarding` / `PermitListen` |
| `remote_bind_not_loopback` | sshd forced the Published Port onto a non-loopback address | Set `GatewayPorts no` or `clientspecified` |
| `remote_bind_unverified` | the actual remote bind could not be verified | Check Linux procfs access and the `GatewayPorts` policy |
| `local_port_reserved` | strict imported intent conflicts with a Published Local Port | Choose another `--local` port or remove one intent |
| `transport_unavailable` | shared SSH transport is unavailable | Existing SSH remediation |

Do not reuse `local_port_conflict`; that diagnostic identifies the machine on
which the bind failed and is part of the current user interface.

## Manager IPC and compatibility

Bump `managerProtocolVersion` from 3 to 4 because both intent and status gain
direction-aware fields. The existing connection flow already replaces an
incompatible or wrong-version Manager, so the rollout uses the current recovery
path.

Update `managerMatches` to reconstruct and compare both persistent collections:

- non-automatic `remote_to_local` statuses become Remembered Forwards;
- `local_to_remote` statuses become Published Forwards;
- Automatic Forwards are excluded as today.

The Unix socket remains user-only and the request body remains bounded with
unknown fields rejected.

## Discovery interaction

The remote scanner reports complete Development Host listener snapshots. An
SSH-created Published Port can appear in `/proc/net/tcp` and may be attributed
to sshd rather than the SSH user. Do not depend on it being hidden.

Core should treat desired Published Ports and active Published Ports
differently:

- exclude every desired Published Port from listener-driven Automatic Forward
  selection, preventing an SSH-created listener from creating a forwarding
  loop;
- filter only active Published Ports from the `Status.Listeners` snapshot, so
  the raw `listeners` JSON field continues to mean discovered application
  listeners.

Prefer this filtering in Core before publishing `Status`; it gives human and
JSON callers the same invariant.

If a real Development Host application already occupies the requested
Published Port, the `-R` request fails and the existing listener remains visible
as Available. A failed Published Forward must not hide that real listener.

## Security model

The product must always generate both addresses explicitly:

```text
-R 127.0.0.1:REMOTE:127.0.0.1:LOCAL
```

There is no `--bind`, wildcard, empty bind address, or arbitrary target host.
Because `GatewayPorts yes` overrides client-requested addresses with wildcard,
the Adapter verifies the actual socket before readiness and immediately cancels
unsafe binds. If control cancellation of an installed forward fails, the
Adapter tears down the product-owned master so no listener on that connection
can survive; a rejected forward request does not trigger cancellation.
Reconciliation restores still-desired forwards on a fresh master.
`GatewayPorts no` and `clientspecified` preserve the requested loopback address.

Important limitations must be documented:

- Development Host loopback is host-local, not user-private. Other users or
  processes on a shared Development Host may be able to connect to the
  Published Port.
- TCP forwarding adds no application authentication. The Local Service's own
  trust model still applies.
- Chrome DevTools grants inspection and control of the browser profile. Use a
  dedicated Chrome profile, do not browse sensitive sites in it, and publish
  the port only to a trusted single-user Development Host.
- The local Manager can connect to the configured Local Service even if that
  service is not otherwise exposed off-machine.

`SECURITY.md` states that both directions use loopback and that Development
Host loopback is machine-local rather than user-private.

Recommended optional sshd hardening for a dedicated development account:

```text
AllowTcpForwarding remote
PermitListen 127.0.0.1:9222
GatewayPorts no
```

These are operator settings, not changes made by `ssh-forward`.

## Example: Chrome DevTools MCP

Run Chrome locally with loopback remote debugging, publish that port with
`ssh-forward publish 9222`, and configure the MCP process on the Development
Host with `--browser-url=http://127.0.0.1:9222`. The MCP remains a remote stdio
process; only its TCP connection to Chrome traverses the Published Forward.

## Code change map

### `internal/core`

- `status.go`: add Direction, PublishedForward, intent fields, and
  direction-aware status fields; make `Backend.Forward` exact-target.
- `manager.go`, `desired.go`, and `reconcile.go`: replace numeric worker/status
  keys with private composite keys, build one desired-state map, reconcile both
  directions, own fallback and local reservations, and filter Published Ports
  from listener-driven behavior.
- `manager_test.go`: test behavior only through Manager and the fake Backend
  seam.

### `internal/openssh`

- `master.go`: make dynamic `forward` / `cancel` direction-aware and clear
  inherited forwarding directives when the private master starts; give mux
  clients an explicit isolated control-socket path.
- `adapter.go`, `forward.go`, and `process.go`: format one exact `-L` or `-R`,
  keep their readiness policies private, remove transport-owned fallback, and
  classify bind failures.
- tests: use an executable fixture to assert argument vectors, plus the real SSH
  integration suite for transport behavior.

### `internal/app`

- `config.go`, `config_normalize.go`, and `config_forwards.go`: schema 5,
  Published Forward normalization and mutation, and host intent loading.
- `ipc.go` and `ipc_client.go`: protocol 4 and the expanded bounded
  intent/status payload.
- `manager.go`: include Published Forwards in Manager equivalence.
- `doctor.go`: group failed Published Ports and provide direction-specific
  advice.

### `internal/cli` and `internal/statusview`

- `command_forward.go`: add `publish` and `unpublish` with strict port parsing.
- `forward_output.go` and `status_output.go`: add stable human and JSON mutation
  results and preserve the legacy JSON shape for existing Forwards.
- `statusview.go` and `format.go`: render Published Forward sections separately
  and never link remote endpoints.
- primer and help: explain both directions in one sentence.

### Documentation and integration fixture

- Update `README.md`, `ARCHITECTURE.md`, `CONTEXT.md`, and `SECURITY.md`.
- Extend the Docker sshd fixture with remote-forward tests while retaining
  `GatewayPorts=no`.

## Verification plan

### Core behavior

- A Published Forward starts without waiting for a discovered listener.
- Same numeric port in opposite directions creates two distinct workers.
- An equivalent intent update creates no worker events.
- Changing a Published Port restarts only its worker.
- Removing a Published Forward normally cancels only its worker; a failed
  control cancellation tears down the shared master as a fail-closed fallback.
- Local fallback skips every Published Local Port reservation.
- Adding a reservation relocates an active fallback binding without disturbing
  its Published Forward.
- Strict imported intent on a reserved Local Port reports
  `local_port_reserved`.
- Backend failure becomes failed state and retries.
- Manager close cancels all workers before Adapter close.
- A Published Port cannot create an Automatic Forward from a scanner snapshot.
- Status sorting is deterministic: existing remote-to-local first by Remote
  Port, then local-to-remote by Local Port.

### Configuration and CLI

- Schema 1-4 migration produces no Published Forwards and preserves existing
  bytes semantically.
- Schema 5 defaults omitted Remote Port to Local Port.
- Duplicate Local Service and duplicate Published Port are rejected per Host.
- A strict Remembered Forward cannot prefer a Published Local Port; a
  fallback-enabled Remembered Forward can.
- Mutations preserve other Hosts, Remembered Forwards, and rules.
- `publish` and `unpublish` are idempotent where intended and return stable JSON.
- Existing `add`, `remove`, status JSON, and help tests remain unchanged except
  for intentional additive help text.

### Real SSH integration

- A Development Host client receives bytes from a Go TCP echo listener on the
  local test machine through `-R`.
- User-configured `LocalForward` and `RemoteForward` entries are not opened by
  the product-owned master or repeated by dynamic control requests.
- The remote listener is bound to `127.0.0.1`, not wildcard.
- A second sshd with `GatewayPorts=yes` forces wildcard, which the Adapter
  detects, cancels, and reports without reaching active state.
- A remote bind conflict reaches failed state with a bounded diagnostic.
- Stopping and restarting the local target does not remove or reallocate the
  remote listening port.
- Canceling one Published Forward leaves imported and other published Forwards
  alive on the shared master.
- Killing the master causes every desired Forward to recover.

### Quality gates

- `scripts/check`
- `scripts/test-integration`
- Core Manager benchmarks with a mixed 256-Forward set to check allocation and
  lock behavior.
- Native Go fuzz targets for JSONC/schema normalization, reservation safety,
  and random reconciliation sequences.

## Delivery sequence

Implement in reviewable vertical steps:

1. **Core model and state machine**: direction-aware types, composite identity,
   desired-state reconciliation, fake Backend tests.
2. **OpenSSH transport**: `-R` request/cancel, readiness, diagnostics, and real
   integration coverage.
3. **Persistence and IPC**: schema 5, config mutations, protocol 4, and Manager
   equivalence.
4. **User interface**: publish/unpublish, status sections, JSON compatibility,
   doctor output, and help.
5. **Product contract and security docs**: update architecture, glossary,
   README workflow, security policy, and skill instructions.

Each step should leave all existing remote-to-local tests green. Do not ship a
hidden config-only reverse mode before status and security behavior are present.

## Alternatives considered

### A second reverse Manager or SSH connection

Rejected. It duplicates Host resolution, credentials, service lifecycle,
reconnection, error handling, and status. The current product-private master is
already the correct external seam and supports dynamic `-R` requests.

### Put `RemoteForward` in `~/.ssh/config`

Useful as a manual workaround, but not the product feature. It is invisible to
intent reconciliation and status, has no product diagnostics, can collide with
product-owned dynamic requests, and couples feature lifetime to undocumented
OpenSSH config side effects.

### Add `--reverse` to `add` and `remove`

Rejected. Positional `PORT` currently means Remote Port, while it would mean
Local Port under the flag. Dedicated verbs keep the daily interface explicit
and make errors and documentation easier to read.

### Replace both config collections with one generic direction-tagged list

Rejected for the first release. It makes schema migration, normalization, and
the compact legacy JSON contract more complex without reducing the user's
concept count. Core may still normalize both collections into one private
desired-state representation.

### Automatic local listener discovery

Deferred. Existing discovery is a Linux-remote feature with remote procfs and
working-directory semantics. Cross-platform local discovery would be a
separate product capability and is unnecessary for explicit local-port
publishing.

## Acceptance criteria

The feature is complete when all of the following are true:

- `ssh-forward publish 9222` makes local `127.0.0.1:9222` reachable at
  Development Host `127.0.0.1:9222` and survives Manager/SSH recovery.
- `ssh-forward unpublish 9222` removes that remote listener without disturbing
  other Forwards.
- The remote listener is verified as loopback before active state; an sshd
  wildcard override is canceled and reported.
- A conflict or sshd restriction is visible as a failed Published Forward with
  actionable bounded text.
- Existing schema 1-4 files and current command behavior remain compatible.
- Status and JSON unambiguously distinguish both directions.
- One shared OpenSSH master carries Discovery and all Forwards.
- Unit, integration, quality, and mixed-load benchmark gates pass.

## References

- [OpenSSH `ssh(1)` remote forwarding and mux control commands](https://man.openbsd.org/ssh)
- [OpenSSH portable mux protocol](https://github.com/openssh/openssh-portable/blob/master/PROTOCOL.mux)
- [OpenSSH `ClearAllForwardings`](https://man.openbsd.org/ssh_config#ClearAllForwardings)
- [Chrome DevTools MCP: connecting to a running Chrome instance](https://github.com/ChromeDevTools/chrome-devtools-mcp#connecting-to-a-running-chrome-instance)
- [Chrome remote-debugging security changes](https://developer.chrome.com/blog/remote-debugging-port)
