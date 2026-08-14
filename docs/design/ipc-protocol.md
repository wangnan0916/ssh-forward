# Local IPC protocol

## Transport and framing

The manager accepts a current-user-only Unix domain socket on macOS/Linux and named pipe on Windows. Go uses pinned `github.com/creachadair/jrpc2`; every frame is one bounded, compact UTF-8 JSON-RPC 2.0 object followed by LF. Swift uses Foundation `Codable` and the same golden wire fixtures. Domain types never marshal directly onto the wire.

## Session handshake

`system.hello` must be the first request. It negotiates protocol major/minor, capabilities, cancellation shape, and maximum frame bytes. Protocol v1 begins at `1.0`, with an initial maximum frame size of 1,048,576 bytes. The server replies with its negotiated version, only the capabilities it currently implements, and `max_frame_bytes`; the initial implementation negotiates no optional capabilities.

The Adapter handles hello synchronously before starting generic JSON-RPC dispatch, so pipelined requests cannot overtake negotiation. A manager or built-in method before hello returns code `-32001` with `data.kind = "hello_required"` and closes the session. An incompatible major returns code `-32002` with `data.kind = "incompatible_protocol"`, the supported version, and closes. A higher same-major minor negotiates down to the server minor. Minor behavior is capability-gated.

```json
{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["cancel-v1","watch-snapshot-v1"]}}
{"jsonrpc":"2.0","id":"1","result":{"protocol":{"major":1,"minor":0},"capabilities":[],"max_frame_bytes":1048576}}
```

JSON-RPC request IDs correlate one attempt. Mutating commands additionally carry `operation_id`, retained across retries and deduplicated for the Manager lifetime.

## Methods

- `manager.execute` — translate one wire command into `Manager.Execute`.
- `manager.snapshot` — return a complete scoped Snapshot. The initial all-host request is `{"scope":{"kind":"all"}}`; a new Manager returns `{"snapshot":{"revision":0}}`.
- `manager.watch` — return an initial complete Snapshot and a `watch_id`, then emit coalesced `manager.snapshot` notifications with increasing revisions.
- `manager.unwatch` — end a Watch explicitly.
- `system.cancel` — best-effort cancellation using the negotiated versioned shape.

Each watcher keeps bounded latest-value state; it is not a durable event log. A slow or disconnected client receives `manager.resync_required` or reconnects for a fresh complete Snapshot. Revisions may skip because intermediate states are coalesced.

## Errors and safety

JSON-RPC standard codes cover parse, invalid request, unknown method, and invalid parameters. Application failures use stable server codes plus `data.kind`, `retryable`, safe details, and a human diagnostic; Go error strings and domain structs are never exposed as contracts. Frames, nesting, strings, collections, and pending requests are bounded. A frame larger than the negotiated/server maximum is closed before unbounded accumulation, and invalid UTF-8 is rejected before JSON decoding. Batch arrays are not part of this protocol: the server returns JSON-RPC `-32600` and closes. Until a notification capability is negotiated, client notifications likewise return `-32600` and close. The initial Adapter admits at most 64 pending calls and executes at most eight handlers concurrently, applying stream backpressure beyond those bounds. Unsupported object fields may be accepted additively within a compatible major, but unknown enum values such as an unsupported Scope kind are rejected explicitly.

Shared Go/Swift fixtures live under `test/protocol/`, beginning with v1 hello and empty-Snapshot transcripts. The corpus grows to cover command success, every typed error, cancellation races, Watch startup, slow-reader resync, malformed and oversized frames, reconnect, and version mismatch as those behaviors are implemented.
