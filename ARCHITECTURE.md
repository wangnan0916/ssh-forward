# Architecture

One user service reconciles forwarding intent independently for every remembered
or discovered SSH host. Imports expose remote loopback services on local IPv4
interfaces; explicit publications expose local services on remote loopback.

```text
CLI typed commands → config.jsonc → POST /v1/reload
                                        ↓
local SSH argv → discovered-hosts.json → managerPool
                                        ↓ one runtime per host
                  listener snapshot → desired state → reconciliation plan
                                        ↓
                             independent forwarding workers
                                        ↓
                     bound OpenSSH adapter → private master

status ← GET /v1/status ← all runtimes (including offline hosts)
```

## Ownership and code map

| Package | Entry points and responsibilities |
| --- | --- |
| `cli` | Kong's typed `commands` grammar dispatches domain `Run` methods. Human status delegates to `statusview`; public JSON has one compatibility projection. |
| `app/config*` | Strict JSONC decoding, schemas 1–6 migration, scoped normalization, atomic rule edits. Configuration is the only source of intent. |
| `app/host_target` | Target identity, option allowlist, exact SSH argv parsing. |
| `app/host_discovery` | Same-user process discovery and product-process exclusion. |
| `app/host_registry` | Locked merge of discovered hosts, explicit hosts, and persistent ignore state. |
| `app/manager_pool` | Runtime composition, global port reservations, targeted reload, sorted all-host status. |
| `app/service*`, `ipc*` | OS service lifecycle, upgrades, bounded HTTP over a user-only Unix socket. |
| `app/doctor*` | Read-only configuration, service, forwarding, and remote discovery checks. |
| `core/desired`, `reconcile` | Pure selection and keep/stop/wait/start planning. |
| `core/manager*`, `worker` | Synchronized status, observation, worker ownership, retry and cleanup. |
| `openssh` | One immutable connection target per adapter, master lifecycle, forwarding, procfs scanner, readiness and bounded diagnostics. |
| `statusview`, `diagnostics` | Human layout and ANSI-aware widths; shared diagnostic descriptions and remediation. |

Libraries handle parsing and mechanisms: Kong, hujson, renameio, ssh_config,
gopsutil/process, flock, doublestar, x/ansi, x/term, kardianos/service, and Go's
HTTP/concurrency primitives. System OpenSSH handles authentication and transport.
Kong is the single command grammar; generated help follows it. The renderer uses
`x/ansi` for grapheme-aware widths and `x/term` for capability detection, without
a widget framework. Tests share `testify/require` assertions and behavioral fixtures.

A supervisor library does not own the forwarding policy: removal must await
cleanup, failures are isolated per port, and replacement cannot overlap a retiring
worker. `sync.WaitGroup.Go` and cancellable contexts express those requirements
without an additional lifecycle framework. The Docker/OpenSSH fixture tests real
transport behavior; parser and planner fuzz tests cover pure boundaries.

## Configuration and discovery

The schema-6 wire format is isolated from the internal map of typed rule scopes.
An empty scope is global; publications require a named scope. Every rule edit
uses the same normalize/validate/save path, including cross-direction conflicts.
Legacy scoped rules stay scoped; `default_host` migrates into explicit hosts.
Invalid configuration leaves existing runtimes intact.

A five-second scan reads native same-user SSH argv, excluding the product process
tree and control commands. Discovered targets persist separately under a
cancellable file lock. Unsupported options produce diagnostic-only candidates;
remote commands and process environments are never persisted. Explicit host
records override discoveries. Ignoring a destination excludes its variants.

Each runtime owns its transport and failures. Only changed/removed targets are
replaced; equivalent intent preserves workers. Published local service ports are
reserved across all runtimes, even when the local application is absent.

## Reconciliation and cleanup

Worker identity is `(direction, service port)`: remote port for imports, local
port for publications. Scoped fixed imports override global listener-port rules,
which override directory matches. Automatic imports disappear with the listener;
fixed imports and publications persist. Desired published remote ports cannot
create automatic imports; only active publications hide available listeners.

Implicit same-port imports and automatic imports may try 20 higher local ports.
Explicit local mappings and all remote publication ports are strict. Actual
fallback ports, listener metadata, and worker/discovery health are volatile.
A publication waits for cleanup of a conflicting import before binding.

Cancellation keeps a worker registered until backend cleanup finishes. Replacement
cannot overlap it, including after rapid listener reappearance. Retry waits are
cancellable; removal never waits for the retry delay. Shutdown waits for all
workers before closing the backend, even if a caller stops waiting.

## Transport boundaries

A master honors the user's connection configuration but uses `-g`, a private
control socket, and `ClearAllForwardings=yes`. Mux clients use `/dev/null` config,
so configured forwards and control paths cannot be inherited. Imports use
`-L 0.0.0.0:LOCAL:127.0.0.1:REMOTE`; publications use
`-R 127.0.0.1:REMOTE:127.0.0.1:LOCAL`.

Imports first check loopback occupancy to prevent macOS split binds. Publications
verify the resulting procfs socket before reporting active: `GatewayPorts yes`
can override a requested bind. Unsafe/unverifiable binds are canceled. Failed
cancellation tears down the private master; a rejected installation does not.
Install and cancel operations check the master generation under the same lock.

Keepalives (5 seconds, 3 unanswered probes) detect silent loss; independent retry
loops reestablish the master, observation, and desired forwards. Commands have
bounded timeouts. Upgrade cleanup retires legacy `master-%C` sockets and old PID
services before rebinding. See [Security](SECURITY.md) for exposure boundaries.
