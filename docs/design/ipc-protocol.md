# Local IPC protocol

## Module seam

The `jsonrpc` module adapts the narrow `core.Manager` interface to one per-user Unix socket. `jrpc2` owns JSON-RPC parsing, request IDs, dispatch, standard errors, concurrency, and notifications. The module owns only the Unix endpoint, bounded line framing, wire DTOs, protocol version, and the Snapshot Watch mapping. Domain types never marshal directly onto the wire.

The Go client checks compatibility with an ordinary `system.version` call before exposing a remote Manager:

```json
{"jsonrpc":"2.0","id":"1","method":"system.version"}
{"jsonrpc":"2.0","id":"1","result":{"version":1}}
```

There is one integer protocol version. A mismatch means the live Manager must be restarted. There is no pre-dispatch handshake, minor-version negotiation, or capability system; new optional behavior should not be added until a real second client requires it.

## Methods

- `system.version` — return `{"version":1}`.
- `manager.snapshot` — no params; return one complete wire Snapshot. A new Manager returns `{"snapshot":{"revision":0}}`.
- `manager.watch` — no params; create a Watch and return its ID plus the initial complete Snapshot.
- `manager.unwatch` — idempotently close one Watch by ID and report whether it was active.

Later complete Snapshots arrive as `manager.snapshot` notifications containing the Watch ID. A Watch is latest-value state, not a durable event log, so revisions may skip. Because `jrpc2` may deliver a server notification concurrently with the subscribe response, clients retain the latest notification for an as-yet-unregistered Watch ID and apply it after the response. This keeps ordering machinery out of the transport Adapter without losing state.

An oversized Snapshot or a Manager-required resync sends `manager.resync_required` and ends that Watch. Any other stream end is silent. `manager.unwatch` is serialized with an in-progress notification write, so no notification for that Watch follows the unwatch response. The Manager's own global Watch limit remains the single resource limit; the IPC Adapter does not add another per-connection policy.

## Transport and errors

Each frame is one compact UTF-8 JSON value followed by LF. Frames are limited to 1 MiB, writes are serialized with a five-second deadline, and invalid UTF-8 or oversized input closes the connection. All other JSON-RPC behavior, including malformed requests, batches, unknown methods, and ordinary request concurrency, is delegated to pinned `jrpc2`. At most eight handlers execute concurrently.

Application errors expose stable `data.kind` and `retryable` fields. Current kinds are `manager_closed` and `watch_limit`; internal Go errors remain private. Snapshot and error wire DTOs stay separate from core domain types.

Shared wire fixtures live under `test/protocol/v2/` and cover the version call, complete Snapshots, Watch start/unwatch, Watch-limit errors, notifications, and resync. Programmatic tests cover bounded framing, Watch cleanup, notification coalescing, and the notification-before-response race.
