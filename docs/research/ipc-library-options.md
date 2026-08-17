# IPC protocol/library options for `ssh-forward`

_Scope: one user-local Go Manager; Go CLI and native Swift client; Unix-domain streams and `go-winio` named pipes. Evidence checked against upstream specifications, repositories, package docs, module files, licenses, and repository activity on 2026-08-14. Later: the Manager Interface is `Snapshot`, `Watch`, and `Close` — there is no `manager.execute`; persistent intent lives in JSONC files (docs/design/ipc-protocol.md)._

## Recommendation

**Use JSON-RPC 2.0 through `github.com/creachadair/jrpc2`, with its newline-delimited channel, rather than owning an ad-hoc request/response envelope.** Keep the protocol deliberately small and implement the Swift peer with Foundation `Codable`; do not add a Swift RPC dependency.

This is a narrow reuse decision, not an attempt to outsource the Manager protocol. `jrpc2` supplies the well-tested generic machinery that is easy to get subtly wrong: JSON-RPC 2.0 request IDs, concurrent request correlation, standard error objects, notifications, bidirectional calls, handler dispatch, context propagation/cancellation, serialized writes, and replaceable stream framing. Its channel API operates on readers/writers rather than choosing a transport, so the same adapter can wrap a Unix `net.Conn` or a `go-winio` pipe. It has a declared stable v1 API, a BSD-3-Clause license, a small module surface, and current repository activity ([README](https://github.com/creachadair/jrpc2/blob/v1.3.5/README.md), [channel API](https://github.com/creachadair/jrpc2/blob/v1.3.5/channel/channel.go), [module file](https://github.com/creachadair/jrpc2/blob/v1.3.5/go.mod), [license](https://github.com/creachadair/jrpc2/blob/v1.3.5/LICENSE), [commits](https://github.com/creachadair/jrpc2/commits/main/)).

The recommendation is conditional on a small interoperability spike proving the exact cancellation extension and line framing against Swift. If that fails, retain the wire sketch below and replace only the Go codec/dispatcher with a small standard-library implementation; do **not** move to gRPC or a plugin framework.

## Important boundary

[JSON-RPC 2.0](https://www.jsonrpc.org/specification) standardizes requests, responses, notifications, IDs, and error object shape. It does **not** standardize:

- framing multiple JSON values on a byte stream;
- transport discovery, authentication, or permissions;
- schema or application-version negotiation;
- semantic idempotency keys;
- cancellation (LSP's `$/cancelRequest` is an extension, not JSON-RPC);
- subscriptions, ordering, backpressure, snapshot revision, replay, or resynchronization.

Consequently `Watch` remains a product protocol: the Manager must coalesce to the newest full `Snapshot`, assign monotonic revisions, define the initial snapshot, detect gaps/reconnects, and require resync rather than pretending JSON-RPC notifications are a durable event log. Wire DTOs and error DTOs remain separate from Manager domain types.

## Viable candidates

### `creachadair/jrpc2` — best fit

| Concern | Assessment |
|---|---|
| JSON-RPC/version | Implements JSON-RPC 2.0. Request IDs are correlation IDs, **not** idempotency keys. |
| Stream framing | The `channel` package makes framing explicit and includes line-oriented JSON plus other channel forms; framing is independent of JSON-RPC ([channel source](https://github.com/creachadair/jrpc2/tree/v1.3.5/channel)). Use one compact JSON value plus LF and impose a maximum frame size. |
| Unix socket / named pipe | Yes: adapt the bidirectional byte stream; it does not depend on TCP or HTTP. `go-winio` exposes named pipes as `net.Conn` ([go-winio pipe API](https://github.com/microsoft/go-winio/blob/main/pipe.go)). |
| Bidirectional / subscriptions | Client notifications and server push are supported. A long-lived `Watch` can therefore acknowledge once, then emit snapshot notifications. Subscription identity and lifetime remain custom. |
| Cancellation | Context-aware calls and server cancellation support are library features, but cancellation is an extension on the wire. Freeze and test the exact method/payload in the project protocol so Swift does not depend on undocumented behavior. Cancellation races and whether an operation is safely retryable remain application concerns. |
| Backpressure | The channel/write path provides safe framing and write serialization, not the required bounded/coalescing policy. Give each watcher a one-element “latest snapshot” slot; a slow client replaces the pending snapshot or is disconnected, never grows an unbounded queue. |
| Negotiation | Library server information is not a product compatibility contract. Require `system.hello` first and negotiate protocol major/minor, capabilities, frame limit, and cancellation method. |
| Swift | JSON-RPC objects and JSON Lines are straightforward with `Codable` and `FileHandle`; no code generation. Swift must implement dispatch, correlation, cancellation, and line buffering, but only for the protocol's few methods. |
| Footprint / license / stability | Small pure-Go dependency, BSD-3-Clause. The README declares the API stable from v1; the repository is active. Pin one version and test its extension behavior before upgrades. |

What it solves: generic RPC envelope validation, dispatch, correlation, errors, concurrent calls, notifications, and Go-side cancellation plumbing. What it does not solve: typed application schemas, compatibility policy, idempotency, Watch semantics, bounded coalescing, reconnect/resync, transport security, or Swift implementation.

### Sourcegraph `jsonrpc2` — credible, but weaker choice

Sourcegraph's package implements JSON-RPC 2.0 over an `io.ReadWriteCloser`; its stream layer offers plain concatenated JSON and VS Code/LSP-style `Content-Length` codecs ([README](https://github.com/sourcegraph/jsonrpc2/blob/master/README.md), [stream source](https://github.com/sourcegraph/jsonrpc2/blob/master/stream.go)). A connection can call and notify in either direction, and `Call` accepts a context ([API source](https://github.com/sourcegraph/jsonrpc2/blob/master/jsonrpc2.go)). It is transport-neutral, so Unix sockets and `go-winio` work.

Its MIT-licensed module has essentially no runtime dependency graph ([go.mod](https://github.com/sourcegraph/jsonrpc2/blob/master/go.mod), [license](https://github.com/sourcegraph/jsonrpc2/blob/master/LICENSE)). Current maintenance is materially better than old impressions suggest: upstream published v0.2.2 in July 2026 and has current commits ([releases](https://github.com/sourcegraph/jsonrpc2/releases), [commits](https://github.com/sourcegraph/jsonrpc2/commits/master/)). However, the API is still pre-v1, its context API should not be assumed to send interoperable wire cancellation, and neither codec gives product backpressure or negotiation. It is viable if zero transitive dependencies dominates, but `jrpc2` has the clearer stability commitment and more explicit channel/cancellation machinery.

### `go.lsp.dev/jsonrpc2` (`go-language-server/jsonrpc2`) — viable only if adopting LSP conventions

This is a JSON-RPC 2.0 implementation shaped around Language Server Protocol usage ([package docs](https://pkg.go.dev/go.lsp.dev/jsonrpc2), [repository](https://github.com/go-language-server/jsonrpc2)). LSP specifies `Content-Length` framing and `$/cancelRequest`, filling two gaps left by base JSON-RPC ([LSP base protocol](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#baseProtocol), [cancellation](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#cancelRequest)). Its stream abstraction can sit on local byte streams and its connection API supports calls/notifications.

It is BSD-3-Clause ([license](https://github.com/go-language-server/jsonrpc2/blob/main/LICENSE)), reached v1.0.1 in June 2026, and has current activity ([releases](https://github.com/go-language-server/jsonrpc2/releases), [commits](https://github.com/go-language-server/jsonrpc2/commits/main)). Its module graph is still small but larger/more LSP-oriented than a tiny codec ([go.mod](https://github.com/go-language-server/jsonrpc2/blob/main/go.mod)). The drawbacks are conceptual: ssh-forward is not an LSP server, LSP cancellation and headers become borrowed protocol surface, and method/schema negotiation plus Watch behavior remain custom. Prefer `jrpc2` unless interoperability with existing LSP tooling is itself a goal.

### Very small custom JSON Lines envelope — acceptable fallback, not first choice

A careful implementation needs only `encoding/json`, a bounded line reader, one writer goroutine, a pending-call map, and per-request contexts. It gives the smallest binary/module graph and complete control, and Swift already needs line decoding. It also avoids pretending that JSON-RPC defines Watch semantics.

The cost is not merely four structs. The project would own duplicate-ID handling, unknown/late responses, concurrent dispatch, notification rules, error-code compatibility, cancellation races, panic isolation, bounded frames, shutdown of pending calls, write serialization, malformed input behavior, and a shared Go/Swift conformance corpus. Those are generic protocol mechanics that `jrpc2` already supplies on the Go side. A custom codec is reasonable only if measurement shows `jrpc2` materially harms idle RSS/binary size, or the Swift interoperability spike exposes unsuitable extension behavior.

## Rejected/heavier comparisons

- **Standard `net/rpc` and `net/rpc/jsonrpc`: reject.** The Go team states that `net/rpc` is frozen and not accepting new features; its method shape is Go-specific, and it has no first-class context cancellation or server event-stream model ([package docs](https://pkg.go.dev/net/rpc)). `net/rpc/jsonrpc` implements **JSON-RPC 1.0**, not 2.0 ([package docs](https://pkg.go.dev/net/rpc/jsonrpc)). Although any `io.ReadWriteCloser` transport can carry it, poor Swift fit and frozen semantics outweigh its zero dependencies.
- **gRPC/protobuf: too heavy here.** It provides generated schemas, typed status, deadlines/cancellation, bidirectional streaming, and HTTP/2 flow control ([core concepts](https://grpc.io/docs/what-is-grpc/core-concepts/), [flow control](https://grpc.io/docs/guides/flow-control/), [cancellation](https://grpc.io/docs/guides/cancellation/)); official Go and Swift stacks are active and Apache-2.0 ([grpc-go](https://github.com/grpc/grpc-go), [grpc-swift](https://github.com/grpc/grpc-swift)). It also adds protobuf toolchains/generated code, HTTP/2 machinery, a much larger dependency/binary surface, and custom dialers for local sockets/pipes. Snapshot revision/resync and version handshake still remain application work.
- **ConnectRPC: also too heavy.** Connect supplies protobuf schemas, generated Go/Swift clients, typed errors, HTTP semantics, and streaming across Connect/gRPC protocols ([protocols](https://connectrpc.com/docs/multi-protocol/), [Go](https://connectrpc.com/docs/go/getting-started/), [Swift](https://connectrpc.com/docs/swift/getting-started/)). Local Unix-socket or pipe use requires custom HTTP transports, and protobuf/code generation remains. Its advantages matter for network services and broad client ecosystems, not one local manager.
- **HashiCorp `go-plugin`: wrong lifecycle.** It is a system for launching and dispensing subprocess plugins over net/rpc or gRPC, including a process handshake and lifecycle management ([README](https://github.com/hashicorp/go-plugin/blob/main/README.md), [license](https://github.com/hashicorp/go-plugin/blob/main/LICENSE)). ssh-forward has an independently discoverable per-user Manager and a native Swift client, not Go plugins to spawn. MPL-2.0 and the substantial stack are unnecessary.
- **Apple XPC: platform mismatch.** XPC/`NSXPCConnection` is an excellent native Apple process boundary with Swift/Objective-C interfaces and system lifecycle integration ([Apple XPC docs](https://developer.apple.com/documentation/xpc), [NSXPCConnection](https://developer.apple.com/documentation/foundation/nsxpcconnection)). It cannot be the shared Linux/Windows protocol or Go CLI transport; adopting it would create a second macOS-only adapter and protocol.

## Minimal dependency and wire sketch

**Go dependencies:** standard `context`, `encoding/json`, `net`; pinned `github.com/creachadair/jrpc2`; Windows-only `github.com/Microsoft/go-winio`. The IPC adapter translates explicit wire DTOs to/from `Manager.Snapshot`, `Watch`, and `Close`; domain packages do not import `jrpc2`.

**Transport/framing:** one authenticated user-local stream; one compact UTF-8 JSON-RPC object per LF; reject frames over a documented limit; exactly one write owner. JSON strings may contain escaped `\n`, never a literal framing newline.

```json
{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"client":"macos","capabilities":["cancel-v1","watch-snapshot-v1"]}}
{"jsonrpc":"2.0","id":"1","result":{"protocol":{"major":1,"minor":0},"session_id":"…","max_frame_bytes":1048576}}

{"jsonrpc":"2.0","id":"2","method":"manager.snapshot","params":{"scope":{"kind":"all"}}}
{"jsonrpc":"2.0","id":"2","result":{"snapshot":{"revision":0}}}

{"jsonrpc":"2.0","id":"3","method":"manager.watch","params":{"scope":{"kind":"all"}}}
{"jsonrpc":"2.0","id":"3","result":{"watch_id":"w1","snapshot":{"revision":42}}}
{"jsonrpc":"2.0","method":"manager.snapshot","params":{"watch_id":"w1","snapshot":{"revision":45}}}
{"jsonrpc":"2.0","method":"manager.resync_required","params":{"watch_id":"w1","reason":"…"}}
```

Rules to freeze in the protocol document and cross-language fixtures:

1. `system.hello` is the first request; incompatible majors fail and close, while minor features use explicit capabilities.
2. JSON-RPC `id` correlates one attempt. Persistent intent is not an RPC command: Forwarding Policies live in `policies.jsonc`.
3. Errors use stable numeric codes plus a typed `data.kind`; prose is diagnostic only. Never expose Go error or domain struct JSON directly.
4. A successful `manager.watch` returns the initial full snapshot. Later notifications are full, monotonically revised snapshots. Revisions may skip because updates are coalesced.
5. Each watcher has bounded latest-value buffering. If correctness cannot be restored from the next full snapshot, send `resync_required` or close; reconnect for a fresh full snapshot.
6. Cancellation uses one explicitly versioned notification shape proven against `jrpc2` and Swift. Cancellation is best-effort; the response/error and idempotency rules define races.
7. Keep golden request/response/error/cancel/watch transcripts shared by Go and Swift, plus malformed-frame, slow-reader, reconnect, and version-mismatch tests.
