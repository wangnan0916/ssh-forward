# Architecture

The product contract is:

> Select one SSH alias, see TCP listeners reachable through its IPv4 loopback,
> and keep remembered remote-to-local forwards plus live listeners matching
> configured working-directory globs available on localhost, while keeping
> explicitly published local services available on the Development Host's
> IPv4 loopback.

Anything that does not serve this sentence is outside the current design.

## Modules

```text
CLI
 ├─ config.jsonc (host, forwards, and directory rules)
 └─ GET /v1/status over a user-only Unix socket
                         │
                      Manager
               ┌─────────┴─────────┐
        observe listeners      one worker per directional Forward
               │                    │
      ssh HOST sh -s       OpenSSH -O forward/cancel
               └──────── shared OpenSSH master ────────┘
```

- `internal/core` owns the forwarding state machine. A pure desired-state
  builder combines persistent intent with listener snapshots, then a pure
  reconciliation planner decides which workers to keep, stop, wait for, or
  start. The Manager executes that plan asynchronously. Its external interface
  is `Status`, `UpdateIntent`, and `Close`; its true-external backend has
  `Observe`, `Forward`, and `Close`.
- `internal/openssh` owns the product-private multiplexed OpenSSH connection,
  dynamic forwarding commands, process invocation, the fixed remote scanner,
  readiness checks, and bounded SSH error classification.
- `internal/app` owns the single config file, SSH alias selection, and the thin
  adapters that compose HTTP/Unix Socket and the user's OS service manager.
  A Manager loads forwarding intent at startup and accepts idempotent intent
  updates over its user-only Unix socket. It reconciles only affected workers;
  `Connect` restarts it when the selected host, protocol, or binary version
  differs from the current process. `Uninstall` removes only the background
  service; persistent intent remains user-owned configuration. Its read-only
  doctor module composes configuration, Manager, and true-remote discovery
  checks without repairing or mutating them.
- `internal/diagnostics` owns the bounded human-readable diagnostic catalog,
  including shared descriptions and doctor remediation text.
- `internal/statusview` owns human status grouping, terminal-width fitting,
  missing-value presentation, and optional ANSI styling and hyperlinks. JSON
  bypasses it.
- `internal/cli` owns command orchestration and edits remembered and published
  forwards; it delegates human status rendering through the
  `statusview.Render` seam.

Mechanisms are delegated to deep external modules: system OpenSSH handles SSH,
`kardianos/service` handles resident process lifecycle, `net/http` handles local
IPC, `ssh_config` parses Host declarations, and `hujson` parses JSONC. Product
code keeps only their composition and the forwarding state machine. Lip Gloss
renders human status tables, while `x/term` detects stdout capabilities.

## State

Persistent state is only:

- one optional default SSH alias;
- a sorted remembered remote-to-preferred-local mapping list, including its
  fallback policy, per alias;
- a sorted published local-to-remote mapping list per alias;
- a sorted absolute working-directory glob list per alias.

Volatile state is rebuilt after restart:

- current remote listeners;
- best-effort listener executable names and working directories;
- ports currently selected by working-directory rules;
- each active Forward's actual local port, which may temporarily differ from
  its preferred port;
- each Published Forward's strict remote listening endpoint;
- discovery health;
- each forward's starting, active, or failed state.

The manager retries discovery and failed forwards. A worker always exists for
every Remembered or Published Forward. It creates a worker for a listener whose
observed working directory matches a configured glob, and cancels that
Automatic Forward when a later complete listener snapshot no longer matches.
Remembered intent wins when both sources select the same Remote Port, so only
one worker exists. Changing a Remembered Forward's preferred Local Port or
fallback policy restarts only that worker. Any Forward with fallback enabled
may try up to 20 higher ports after a local conflict; implicit same-port and
Automatic Forwards enable that policy by default. The selected port remains
volatile. Invalid configuration prevents a new Manager from starting.

Workers use the composite identity `{direction, service port}`: the Remote Port
identifies a remote-to-local Forward and the Local Port identifies a Published
Forward. Core owns local fallback and skips ports reserved as Published local
targets. Published remote ports are strict, bind only to `127.0.0.1` on the
Development Host, and are excluded from listener discovery and Automatic
Forward selection.

The OpenSSH adapter translates directions to exact `-L` and `-R` control
requests on the same product-owned master. The master honors the selected Host
alias for connection setup but starts with `ClearAllForwardings=yes`; later
discovery and multiplexing commands use the explicit private control socket
with `/dev/null` as client config. This prevents `LocalForward`,
`RemoteForward`, or `ControlPath` entries in user configuration from being
silently duplicated while preserving authentication, jump-host, and connection
settings on the master. A successful `-R` request is not sufficient readiness:
the Adapter reads the resulting remote procfs socket and accepts only an actual
IPv4 loopback bind. It cancels wildcard listeners forced by `GatewayPorts yes`
and fails closed when the bind cannot be verified. Failed cancellation of an
installed forward tears down the product-owned master; a rejected forward
request does not. Reconciliation rebuilds affected forwards on a fresh
connection after teardown.

Before creating an alias-hash master, the Adapter also checks the legacy
`master-%C` path using the selected SSH config. If an older product-owned
master is still alive, it requests exit and waits for that mux socket to stop
responding so orphaned listeners cannot conflict with replacement forwards.
