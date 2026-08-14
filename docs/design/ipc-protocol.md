# Local IPC protocol

## Transport and framing

The manager accepts a current-user-only Unix domain socket on macOS/Linux and named pipe on Windows. Go uses pinned `github.com/creachadair/jrpc2`; every frame is one bounded, compact UTF-8 JSON-RPC 2.0 object followed by LF. Swift uses Foundation `Codable` and the same golden wire fixtures. Domain types never marshal directly onto the wire.

## Session handshake

`system.hello` must be the first request. It negotiates protocol major/minor, capabilities, cancellation shape, and maximum frame bytes. Incompatible majors return a stable error and close. Minor behavior is capability-gated.

```json
{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["cancel-v1","watch-snapshot-v1"]}}
```

JSON-RPC request IDs correlate one attempt. Mutating commands additionally carry `operation_id`, retained across retries and deduplicated for the Manager lifetime.

## Methods

- `manager.execute` — translate one wire command into `Manager.Execute`.
- `manager.snapshot` — return a complete scoped Snapshot.
- `manager.watch` — return an initial complete Snapshot and a `watch_id`, then emit coalesced `manager.snapshot` notifications with increasing revisions.
- `manager.unwatch` — end a Watch explicitly.
- `system.cancel` — best-effort cancellation using the negotiated versioned shape.

Each watcher keeps bounded latest-value state; it is not a durable event log. A slow or disconnected client receives `manager.resync_required` or reconnects for a fresh complete Snapshot. Revisions may skip because intermediate states are coalesced.

## Errors and safety

JSON-RPC standard codes cover parse, invalid request, unknown method, and invalid parameters. Application failures use stable server codes plus `data.kind`, `retryable`, safe details, and a human diagnostic; Go error strings and domain structs are never exposed as contracts. Frames, nesting, strings, collections, and pending requests are bounded. Unsupported fields and versions follow explicit compatibility rules.

Shared Go/Swift fixtures cover hello, command success, every typed error, cancellation races, Watch startup, slow-reader resync, malformed and oversized frames, reconnect, and version mismatch.
