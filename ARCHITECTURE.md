# Architecture

The product contract is:

> Select one SSH alias, see its IPv4 loopback TCP listeners, and remember a
> port so it stays mapped to the same port on localhost.

Anything that does not serve this sentence is outside the current design.

## Modules

```text
CLI
 ├─ config.jsonc (default host and remembered ports)
 └─ Manager.Status over a user-only Unix socket
                         │
                      Manager
               ┌─────────┴─────────┐
        observe listeners      one worker per port
               │                    │
      ssh HOST sh -s       ssh -N -L local:remote
```

- `internal/core` owns the small state machine. Its external interface is
  `Status` and `Close`; its true-external backend has `Observe` and `Forward`.
- `internal/openssh` owns process invocation, the fixed remote scanner,
  readiness checks, and bounded SSH error classification.
- `internal/app` owns the single config file, SSH alias selection, background
  process lifecycle, PID file, and Unix socket composition.
- `internal/jsonrpc` exposes only protocol version and manager status.
- `internal/cli` formats status and edits remembered ports.

## State

Persistent state is only:

- one optional default SSH alias;
- a sorted port list per alias.

Volatile state is rebuilt after restart:

- current remote listeners;
- discovery health;
- each forward's waiting, starting, active, or failed state.

The manager keeps the last valid remembered-port set if `config.jsonc` is
temporarily invalid. It retries discovery and failed forwards. A port worker
exists only while that port is both remembered and observed listening.

## Deliberate omissions

There is no generic policy language, process or directory attribution,
wildcard-listener handling, port fallback, custom TCP proxy, SOCKS data plane,
revision log, server-side watch stream, or multi-host runtime. New behavior
should not introduce one of those concepts without first changing the product
contract above.
