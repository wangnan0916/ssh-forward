# Architecture

The product contract is:

> Select one SSH alias, see its IPv4 loopback TCP listeners, and remember a
> port so it stays mapped to the same port on localhost.

Anything that does not serve this sentence is outside the current design.

## Modules

```text
CLI
 ├─ config.jsonc (default host and remembered ports)
 └─ GET /v1/status over a user-only Unix socket
                         │
                      Manager
               ┌─────────┴─────────┐
        observe listeners      one worker per remembered port
               │                    │
      ssh HOST sh -s       ssh -N -L local:remote
```

- `internal/core` owns the small state machine. Its external interface is
  `Status` and `Close`; its true-external backend has `Observe` and `Forward`.
- `internal/openssh` owns process invocation, the fixed remote scanner,
  readiness checks, and bounded SSH error classification.
- `internal/app` owns the single config file, SSH alias selection, and the thin
  adapters that compose HTTP/Unix Socket and the user's OS service manager.
  A Manager loads its immutable port set at startup; `Connect` restarts it when
  the selected host or configured ports differ from its current status.
- `internal/cli` formats status and edits remembered ports.

Mechanisms are delegated to deep external modules: system OpenSSH handles SSH,
`kardianos/service` handles resident process lifecycle, `net/http` handles local
IPC, `ssh_config` parses Host declarations, and `hujson` parses JSONC. Product
code keeps only their composition and the forwarding state machine.

## State

Persistent state is only:

- one optional default SSH alias;
- a sorted port list per alias.

Volatile state is rebuilt after restart:

- current remote listeners;
- discovery health;
- each forward's starting, active, or failed state.

The manager retries discovery and failed forwards. A port worker exists for
every Remembered Port; OpenSSH keeps the local listener useful across remote
process restarts. Invalid configuration prevents a new Manager from starting.

## Deliberate omissions

There is no generic policy language, process or directory attribution,
wildcard-listener handling, port fallback, custom TCP proxy, SOCKS data plane,
revision log, server-side watch stream, runtime config reload, or multi-host
runtime. New behavior should not introduce one of those concepts without first
changing the product contract above.
