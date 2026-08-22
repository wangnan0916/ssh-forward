# Local daemon IPC library audit

_Checked against the repository and primary upstream sources on 2026-08-21. Scope: one per-user Go Manager, short-lived Go CLI clients, and a possible native Swift desktop client. No production code was changed for this audit._

> **Implementation outcome (2026-08-21):** The audit's reduction was applied after the research snapshot. The Adapter now uses an ordinary `system.version` RPC and delegates request validation, batching, dispatch, and concurrency to `jrpc2`. Capability negotiation, pre-dispatch hello, Scope compatibility, custom request-admission slots, and the response-written framing hook were removed. Production IPC code fell from 1,319 to 833 lines; the baseline inventory below is retained to explain the decision.

## Short answer

Yes, mature RPC libraries exist, and this project already uses one: `github.com/creachadair/jrpc2` v1.3.5. The project is **not** reimplementing JSON-RPC request dispatch and response correlation from scratch.

No mature library can replace the whole current `internal/jsonrpc` plus `app/singleton.go`, because they contain three different concerns:

1. **Generic RPC machinery** — envelope parsing, request IDs, dispatch, errors, concurrent calls, and notifications. A library should own this; `jrpc2` already does.
2. **Product protocol** — `system.hello`, capabilities, Snapshot DTOs, Watch semantics, revisions, latest-value coalescing, and resynchronization. The project must define this regardless of the RPC stack.
3. **Daemon lifecycle** — one process per user, stale socket handling, permissions, PID ownership, detached launch, readiness, logs, restart, and shutdown. An RPC library does not own this. An OS service manager can own much of it once installation/login-start behavior is part of the product.

The immediate recommendation is therefore:

> **Keep `jrpc2`; do not migrate IPC now. Freeze the protocol and simplify its product requirements before simplifying its implementation. Reconsider gRPC only when a Swift client is committed, and move long-running process supervision to `launchd`/systemd rather than to another RPC framework.**

## What the repository owned at the audit baseline

The pinned dependency is visible in [`cli/go.mod`](../../cli/go.mod). The six production files under [`cli/internal/jsonrpc`](../../cli/internal/jsonrpc/) total 1,319 lines:

| File | Lines | Responsibility |
|---|---:|---|
| `adapter.go` | 93 | Map the `core.Manager` interface to RPC handlers |
| `channel.go` | 240 | Bounded LF framing, UTF-8 and batch checks, write deadlines, request admission |
| `client.go` | 260 | Remote `core.Manager`, call/error translation, client Watch stream |
| `endpoint.go` | 123 | Unix socket liveness, stale-file replacement, permissions, accept loop |
| `protocol.go` | 346 | Hello/version/capability handshake and wire DTOs |
| `watch.go` | 257 | Watch ownership, subscribe-before-notify ordering, limits, unwatch/resync |

The corresponding tests total 1,278 lines. [`cli/internal/app/singleton.go`](../../cli/internal/app/singleton.go) adds another 402 production lines, but it is process supervision and application assembly rather than JSON-RPC.

`jrpc2` already provides the generic client and server, JSON-RPC 2.0 errors, method dispatch, concurrent calls, context plumbing, client notifications, optional server push, and several channel framings. It declares its v1 Go API stable; v1.3.5 was published on 2026-03-04 and is the current tag ([project documentation](https://pkg.go.dev/github.com/creachadair/jrpc2), [tags](https://github.com/creachadair/jrpc2/tags), [channel API](https://pkg.go.dev/github.com/creachadair/jrpc2/channel)).

The remaining code is not all accidental duplication:

- `jrpc2`'s `channel.Line` has no maximum-record size or socket write deadline; its implementation reads until LF. The current bounded channel is therefore an application hardening wrapper, not a second JSON-RPC implementation ([upstream `channel/split.go`](https://github.com/creachadair/jrpc2/blob/v1.3.5/channel/split.go)).
- JSON-RPC 2.0 defines calls, notifications, responses, IDs, and error objects, but not byte-stream framing, transport permissions, cancellation, subscription semantics, capability negotiation, replay, or resynchronization ([JSON-RPC 2.0 specification](https://www.jsonrpc.org/specification)).
- `jrpc2` explicitly describes server push as a non-standard extension and says client context cancellation is not automatically propagated to the server ([jrpc2 package documentation](https://pkg.go.dev/github.com/creachadair/jrpc2#hdr-Server_Push), [cancellation documentation](https://pkg.go.dev/github.com/creachadair/jrpc2#hdr-Contexts_and_Cancellation)).

This means a library swap cannot honestly promise to delete all 1,319 lines. It can change where the remaining protocol code sits and how much transport behavior comes for free.

## Candidate comparison

| Option | Generic calls | Native Watch primitive | Go/Swift contract | Owns daemon lifecycle | Verdict |
|---|---|---|---|---|---|
| `creachadair/jrpc2` | Strong | Notifications, but semantics remain custom | JSON DTOs; Swift peer is handwritten | No | Keep now |
| Go `net/rpc` | Basic, frozen | No | Go-shaped API; JSON codec is JSON-RPC 1.0 | No | Reject |
| gRPC + protobuf | Strong, generated | Server streaming with cancellation/flow control | Official Go and Swift stacks | No | Best only after Swift is committed |
| HashiCorp `go-plugin` | `net/rpc` or gRPC | Via underlying gRPC | Cross-language possible but plugin handshake is extra | Owns a child plugin, not an independent user daemon | Wrong lifecycle |
| D-Bus / `godbus` | Methods, errors, signals | Signals | Good on D-Bus desktops; weak macOS/Swift product fit | Bus activation can help | Reject for this product |
| `net/http` over UDS | Strong HTTP request lifecycle | Streaming body/SSE, semantics custom | Go is simple; Swift needs a UDS-capable transport | No | Credible Go-only simplification, not worth migrating now |

### 1. `creachadair/jrpc2`: the correct current dependency

What it solves:

- JSON-RPC 2.0 message validation and standard error codes;
- request IDs and response correlation;
- handler lookup and concurrent execution;
- concurrent Go client calls;
- notifications and optional server-to-client push;
- pluggable framing over any `io.Reader`/`io.WriteCloser`;
- cancellation of local client waits and handler contexts when a server stops.

The upstream `server` package can also run one server per accepted connection, although using it here would only replace a small accept-loop portion because the project performs `system.hello` before starting the generic dispatcher ([`server.Loop` source](https://github.com/creachadair/jrpc2/blob/v1.3.5/server/loop.go)).

What it cannot solve:

- socket location, file mode, stale socket cleanup, or peer authorization;
- exactly-one-manager ownership and daemon launch/restart;
- product compatibility policy (`major`, `minor`, capabilities);
- typed Swift code generation;
- whether Watch sends deltas or full snapshots;
- latest-value coalescing, revision gaps, reconnect, and resync.

Assessment: replacing `jrpc2` with another JSON-RPC library would be churn, not simplification. The useful next step is to decide which current protocol guarantees are required by a real client. In particular, the pre-dispatch hello, rejection of every client notification/batch, exact subscribe-response ordering, and multiple simultaneous Watch IDs are defensible hardening, but they are also where much of the wrapper complexity lives. They should not expand until a second client exercises them.

### 2. Go `net/rpc`: standard library, but a regression

`net/rpc` is mature in the sense that it is old and stable, but the Go project explicitly says it is frozen and not accepting features. Its exported method shape is Go-specific, it has no first-class `context.Context` or streaming RPC, and the standard `net/rpc/jsonrpc` codec implements JSON-RPC 1.0 rather than 2.0 ([`net/rpc` documentation](https://pkg.go.dev/net/rpc), [`net/rpc/jsonrpc` documentation](https://pkg.go.dev/net/rpc/jsonrpc)).

It would work over a Unix socket, but Watch would require a second connection, polling, or another custom protocol. A Swift client would have to reproduce Go-oriented conventions with no official support. Zero third-party dependencies do not compensate for those limitations.

Assessment: reject.

### 3. gRPC-Go + gRPC Swift 2: the strongest cross-language option

gRPC provides the largest real reduction in protocol plumbing:

- one `.proto` contract and generated Go/Swift types and stubs;
- unary `Snapshot`;
- server-streaming `Watch`, with ordering within the RPC;
- standard cancellation, deadlines, status codes, metadata, HTTP/2 multiplexing, and transport flow control.

These are first-class gRPC concepts, not project conventions ([gRPC core concepts](https://grpc.io/docs/what-is-grpc/core-concepts/), [flow control](https://grpc.io/docs/guides/flow-control/), [cancellation](https://grpc.io/docs/guides/cancellation/)). gRPC-Go accepts `unix:///path/to/socket` targets, and the official current Swift stack is `grpc-swift-2` plus its SwiftNIO transport ([gRPC-Go target examples](https://github.com/grpc/grpc-go/blob/v1.83.1/clientconn.go), [gRPC Swift 2 repository](https://github.com/grpc/grpc-swift-2), [Swift NIO transport](https://github.com/grpc/grpc-swift-nio-transport)). Current maintenance is strong: gRPC-Go v1.83.1 was released on 2026-08-19 and gRPC Swift 2.4.2 on 2026-06-23 ([gRPC-Go release](https://github.com/grpc/grpc-go/releases/tag/v1.83.1), [gRPC Swift release](https://github.com/grpc/grpc-swift-2/releases/tag/2.4.2)).

What still remains custom:

- daemon discovery and startup;
- UDS permissions and stale endpoint handling;
- application compatibility policy beyond protobuf wire compatibility;
- whether intermediate Snapshots may be discarded;
- revision and resync rules after disconnection;
- mapping domain failures to stable public errors.

Costs relevant to this project:

- protobuf and gRPC code generation become part of the build;
- Go and Swift gain materially larger dependency graphs;
- the current gRPC-Go module requires Go 1.25, which this repository satisfies, while the current Swift 2 packages use Swift tools 6.1 and declare macOS 15 availability ([gRPC-Go `go.mod`](https://github.com/grpc/grpc-go/blob/v1.83.1/go.mod), [gRPC Swift package](https://github.com/grpc/grpc-swift-2/blob/main/Package.swift), [Swift NIO transport package](https://github.com/grpc/grpc-swift-nio-transport/blob/main/Package.swift));
- the Swift NIO transport has a current UDS `:authority` interoperability issue; the upstream issue documents an explicit `localhost` authority workaround ([upstream issue](https://github.com/grpc/grpc-swift-nio-transport/issues/176)).

Assessment: gRPC is not overengineering if the native Swift client is a committed near-term deliverable. It is overengineering for one Go daemon and one Go CLI. Do not migrate speculatively.

### 4. HashiCorp `go-plugin`: mature, but models the wrong owner

`go-plugin` is production-proven and current (v1.8.0, 2026-04-29). It launches a plugin subprocess, performs a stdout handshake, negotiates `net/rpc` or gRPC, connects to the child, mirrors logs/TTY, kills the child, and can reconnect with a `ReattachConfig` ([architecture and features](https://github.com/hashicorp/go-plugin), [handshake internals](https://github.com/hashicorp/go-plugin/blob/main/docs/internals.md), [v1.8.0 release](https://github.com/hashicorp/go-plugin/releases/tag/v1.8.0)).

Its normal ownership model is:

```text
long-lived host process -> starts and owns plugin subprocess
```

This project's model is:

```text
independent per-user Manager <- many short-lived CLI processes and a future app
```

`ReattachConfig` does not change that mismatch; the upstream README says host upgrade/reattach requires the process to daemonize properly. A Swift client would also need the go-plugin handshake/control conventions in addition to ordinary gRPC stubs. If the product later changes so a desktop app exclusively owns a private Go helper, `go-plugin` becomes plausible. It is not the right abstraction for a shared, independently discoverable Manager.

Assessment: reject under the current lifecycle.

### 5. D-Bus with `godbus`: rich desktop IPC, wrong platform baseline

D-Bus standardizes method calls, errors, object/interface names, signals, bus names, authentication, and service activation. Signals could carry Snapshot updates, and a session bus can start an activatable service when a client first addresses its well-known name ([D-Bus specification](https://dbus.freedesktop.org/doc/dbus-specification.html), [service activation](https://dbus.freedesktop.org/doc/dbus-daemon.1.html#INTEGRATING_SESSION_SERVICES)). `github.com/godbus/dbus/v5` is a native Go implementation with method export, asynchronous calls, and signal channels; v5.2.2 was published in December 2025 ([package documentation](https://pkg.go.dev/github.com/godbus/dbus/v5@v5.2.2)).

However:

- D-Bus requires a bus daemon/session environment and its activation files;
- it is a natural dependency on Linux desktops, not the native macOS baseline;
- the D-Bus specification describes launchd address discovery on macOS, but that still presumes an installed/configured D-Bus session bus ([D-Bus transports](https://dbus.freedesktop.org/doc/dbus-specification.html#transports-launchd));
- the `godbus` README still describes its API as unstable despite the v5 module path ([upstream README](https://github.com/godbus/dbus));
- a native Swift client does not gain the official generated contract and ecosystem that gRPC provides;
- D-Bus signals are not a durable log, so initial Snapshot, coalescing, revision, and resync semantics remain product work.

Assessment: reject unless the product becomes Linux-desktop-first.

### 6. Standard-library HTTP over a Unix socket: the credible minimalist alternative

The Go standard library can serve HTTP on any `net.Listener`, including a Unix listener, and `http.Transport.DialContext` can dial a UDS. Request contexts handle client disconnect/cancellation; response streaming is available through `http.Flusher` ([`http.Server.Serve`](https://pkg.go.dev/net/http#Server.Serve), [`http.Transport.DialContext`](https://pkg.go.dev/net/http#Transport), [`http.Flusher`](https://pkg.go.dev/net/http#Flusher), [Go Unix socket implementation](https://go.dev/src/net/unixsock.go)). A minimal protocol could be:

```text
GET /v1/snapshot -> one JSON response
GET /v1/watch    -> JSON Lines streaming response; first line is the full Snapshot
```

This delegates framing, concurrent connections, status codes, request cancellation, header limits, and response ordering to `net/http`. The application still owns JSON DTOs, compatibility, latest-value coalescing, revisions/resync, socket permissions, and daemon startup.

For a Go-only product this is probably the smallest understandable stack. For the stated Go-plus-native-Swift direction, it is less clearly simpler: choosing Go's `net/http` does not supply the Swift-side UDS transport. That client would still need a Unix-socket-capable stack such as SwiftNIO (whose socket API supports Unix domain paths) or a different local transport ([SwiftNIO Unix socket address](https://github.com/apple/swift-nio/blob/main/Sources/NIOCore/SocketAddresses.swift)).

Assessment: keep as a fallback if the project deliberately drops the cross-language IPC requirement. Migrating the already-tested JSON-RPC adapter to HTTP now would trade one custom Watch adapter for another.

## Daemon lifecycle is a separate decision

No RPC candidate above should be selected because it appears to solve process supervision. The mature owner of a persistent process is the operating system's service manager:

- Apple recommends `launchd` for per-user background processes and supports launch-on-demand or continuously kept-alive agents ([Apple daemon/service guide](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)).
- systemd supports socket activation on Linux ([systemd socket activation](https://www.freedesktop.org/software/systemd/man/latest/systemd-socket-activate.html)).
- `github.com/kardianos/service` offers a cross-platform Go API for install/start/stop across launchd, systemd and Windows Services; v1.3.0 was published in July 2026 ([repository](https://github.com/kardianos/service), [package documentation](https://pkg.go.dev/github.com/kardianos/service)).

Those tools can replace detached-child/PID supervision when `ssh-forward install`, “start at login”, or desktop-app installation becomes a real workflow. They do not define Snapshot or Watch, and they do not remove the RPC adapter.

For the current no-install CLI experience, the small self-spawn path may still be the least surprising behavior. It should be treated as temporary lifecycle code, not as evidence that a different RPC library is needed.

## Final recommendation

1. **Now:** keep Go, the resident Manager, UDS, and `jrpc2` v1.3.5. Do not replace a working protocol with `net/rpc`, D-Bus, `go-plugin`, or HTTP merely to reduce dependency count.
2. **Simplify by deleting requirements first:** freeze `internal/jsonrpc`; before adding any capability, prove that a real CLI or Swift client needs it. Re-evaluate whether v1 truly needs multiple Watches per connection, batch rejection, a separate pre-dispatch hello, and all current malformed-peer hardening.
3. **When Swift work actually begins:** build a tiny interoperability spike with two paths: current JSON-RPC over a Swift UDS stream, and gRPC server-streaming over UDS. Choose gRPC if generated types and Watch cancellation remove more complexity than its build/runtime dependencies add.
4. **When persistence becomes installable product behavior:** hand process supervision to launchd/systemd, directly or through a small service wrapper. Keep product protocol and daemon lifecycle as separate modules.

The core conclusion is not “we should implement IPC ourselves.” It is:

> Use a mature library for generic RPC mechanics; keep only the product semantics that no generic library can know. The current project already follows the first half, but may have specified too much of the second half before a second client exists.
