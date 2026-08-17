# Local IPC protocol

## Transport and framing

The manager accepts a current-user-only Unix domain socket on macOS/Linux and named pipe on Windows. Go uses pinned `github.com/creachadair/jrpc2`; every frame is one bounded, compact UTF-8 JSON-RPC 2.0 object followed by LF. Swift uses Foundation `Codable` and the same golden wire fixtures. Domain types never marshal directly onto the wire.

## Session handshake

`system.hello` must be the first request. It negotiates protocol major/minor, capabilities, cancellation shape, and maximum frame bytes. Protocol v1 begins at `1.0`, with an initial maximum frame size of 1,048,576 bytes. The server replies with its negotiated version, only the capabilities it currently implements, and `max_frame_bytes`. The first optional capability is `watch-snapshot-v1`, which permits server Snapshot and resync notifications; it does not permit client notifications.

The Adapter handles hello synchronously before starting generic JSON-RPC dispatch, so pipelined requests cannot overtake negotiation. A manager or built-in method before hello returns code `-32001` with `data.kind = "hello_required"` and closes the session. An incompatible major returns code `-32002` with `data.kind = "incompatible_protocol"`, the supported version, and closes. A higher same-major minor negotiates down to the server minor. Minor behavior is capability-gated.

```json
{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["cancel-v1","watch-snapshot-v1"]}}
{"jsonrpc":"2.0","id":"1","result":{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"],"max_frame_bytes":1048576}}
```

JSON-RPC request IDs correlate one attempt.

## Methods

- `manager.snapshot` — return a complete scoped Snapshot. The initial all-host request is `{"scope":{"kind":"all"}}`; a new Manager returns `{"snapshot":{"revision":0}}`. The single `host` entry (present once a Development Host is configured) carries alias, Connection State, Discovery State/Capability with a `diagnostic` explaining partiality or failure (`scanner_reported_partial`, `process_metadata_unavailable`, `evidence_truncated`, a scanner failure reason, or `observation_resync`), baseline and scanner identity, complete deterministically ordered Listener Observations with bounded Process Chains, and complete Forwards.
- `manager.watch` — with `watch-snapshot-v1`, return `{"watch_id":"watch-…","snapshot":…}`. The response is written before that Watch can emit `manager.snapshot` notifications carrying the same `watch_id` and later complete Snapshots.
- `manager.unwatch` — idempotently end one Watch by `watch_id` and report whether it was active. Its response is ordered after any already-started bounded notification write, and no notification for that Watch follows the response.
- `system.cancel` — best-effort cancellation using the negotiated versioned shape.

Each watcher keeps bounded initial/latest-value state; it is not a durable event log. One IPC connection may own at most eight active Watches, and the Manager admits at most 128 globally. Notifications never consume inbound request-admission slots. Revisions may skip because intermediate states are coalesced. An oversized Snapshot or a Manager-required resync yields `manager.resync_required` and ends that Watch; any other stream end stops the Watch silently. If even the small resync notification cannot be delivered, the connection closes and the client reconnects for a fresh complete Snapshot.

## Errors and safety

JSON-RPC standard codes cover parse, invalid request, unknown method, and invalid parameters. Application codes cover `manager_closed` (`-32014`) and retryable `watch_limit` (`-32015`). Calling a Watch method without negotiating `watch-snapshot-v1` returns `-32003` with `data.kind = "capability_required"`. Each application error includes stable `data.kind` and `retryable`. Go error strings and domain structs are never exposed as contracts. Frames, nesting, strings, collections, and pending requests are bounded. A frame larger than the negotiated/server maximum is closed before unbounded accumulation, and invalid UTF-8 is rejected before JSON decoding. Batch arrays are not part of this protocol: the server returns JSON-RPC `-32600` and closes. Client notifications are not supported by any current capability: they return `-32600` and close. The Adapter admits at most 64 pending calls and executes at most eight handlers concurrently, applying stream backpressure beyond those bounds. Outbound responses and notifications share one serialized writer with a five-second write deadline. Unsupported object fields may be accepted additively within a compatible major, but unknown enum values such as an unsupported Scope kind are rejected explicitly.

Shared Go/Swift fixtures live under `test/protocol/`. The v1 corpus covers hello; empty, Managed Forward, and Discovery Snapshots; Watch startup/unwatch; capability and Watch-limit errors; Snapshot notification; and resync. Additional typed errors, cancellation races, malformed and oversized frames, reconnect, and version mismatch have programmatic Go coverage and can gain shared fixtures when a Swift Adapter needs them.
