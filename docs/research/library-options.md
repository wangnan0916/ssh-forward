# Reusable library options for `ssh-forward`

_Date: 2026-08-14. Scope: the settled Go core/CLI and native macOS client architecture. Sources are upstream package documentation, repositories, specifications, and license files._

## Decision summary

Keep the implementation mostly in the Go and Apple standard libraries. The justified Go modules are:

1. A narrow SOCKS5 Dial Adapter, with [`golang.org/x/net/proxy`](https://pkg.go.dev/golang.org/x/net/proxy) retained only if the implementation preserves both context cancellation and TCP half-close;
2. [`github.com/tailscale/hujson`](https://github.com/tailscale/hujson) for comment-preserving JSONC/JWCC;
3. [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) for the small OS-specific surface: Unix locking/process groups and Windows locks, jobs, and process APIs;
4. [`github.com/Microsoft/go-winio`](https://github.com/microsoft/go-winio) only in Windows builds, when named-pipe support is implemented;
5. [`github.com/creachadair/jrpc2`](https://github.com/creachadair/jrpc2) for Go-side JSON-RPC 2.0 dispatch, correlation, errors, notifications, cancellation plumbing, and newline channels;
6. [`github.com/spf13/cobra`](https://github.com/spf13/cobra) for the CLI command tree, nested help, and POSIX/GNU-style flags (see ADR-0020).

Do not add a TCP-proxy package, a heavier RPC stack, a daemon framework, an embedded SSH stack, or a Swift package now. This keeps idle memory, binary size, update surface, and protocol coupling low.

## Evaluation by concern

### CLI parsing: Cobra for the command surface

Go's [`flag`](https://pkg.go.dev/flag) package is enough for a handful of global flags, but the product now has nested commands (`policy list`, `host list`, `manager serve`) and needs the help shape users expect from a mainstream CLI: a command list, per-command `--help`, and flags that may precede or follow the positional port. Hand-rolled `FlagSet` dispatch plus a custom interspersed-flag parser duplicated that work and still produced a help page that did not match the command tree.

[Cobra](https://github.com/spf13/cobra) is Apache-2.0 licensed ([license](https://github.com/spf13/cobra/blob/main/LICENSE.txt)) and uses `pflag` (and, on Windows, `mousetrap`). It owns the command tree and generated help. Composition — host resolution, auto-spawn, and the OpenSSH adapter — stays in `main`, which injects `Bind` / `Assemble` / `ServeManager` so tests can still drive `App.Run` with a fake Manager.

### SOCKS5 client into `ssh -D`: require cancellable half-close

[`proxy.SOCKS5`](https://pkg.go.dev/golang.org/x/net/proxy#SOCKS5) supplies mature RFC 1928 framing and remains the preferred dependency candidate. The transport spike exposed an API mismatch in the tested `x/net` v0.58.0 API that must be resolved before production adoption, however: its legacy `Dial` path returns the raw TCP connection and therefore preserves `CloseWrite`, but does not bound the SOCKS CONNECT handshake; its `ContextDialer.DialContext` path bounds establishment but returns the package's SOCKS connection wrapper, whose public method set does not expose the underlying TCP `CloseWrite`. The proxy pump needs both properties for cancellable Endpoint shutdown and correct response-after-client-EOF protocols.

The formal Transport Adapter must therefore be selected behind behavior tests that prove bounded cancellation, IPv4/IPv6 CONNECT, and TCP half-close. Prefer a maintained library or a small upstreamable adapter; do not use reflection to unwrap private library state. The throwaway spike used a minimal no-auth CONNECT dialer only to establish feasibility, not as an automatic production-code choice. If no narrow maintained client satisfies the Seam, a small audited RFC 1928 Adapter with protocol-limit and malformed-response tests is safer than importing a full proxy stack.

`x/net` is maintained by the Go project and BSD-3-Clause licensed ([license](https://github.com/golang/net/blob/master/LICENSE)). If retained, pin a tested module version and update it with the Go security/update cadence.

### Bidirectional TCP proxy: standard `io`/`net`, with a small custom pump

Use two concurrent [`io.CopyBuffer`](https://pkg.go.dev/io#CopyBuffer) calls and pooled, bounded buffers. Go's `net.TCPConn` exposes [`CloseWrite`](https://pkg.go.dev/net#TCPConn.CloseWrite), while closing a `net.Conn` unblocks blocked I/O. A project-owned helper should:

- copy A→B and B→A concurrently;
- after clean EOF in one direction, call `CloseWrite` on the destination to preserve TCP half-close;
- keep the reverse direction alive until its EOF/error;
- on context cancellation or fatal error, close both connections (or set immediate deadlines) to unblock both goroutines;
- wait for both pumps and normalize expected close/EOF errors;
- record bytes and duration without per-read logging.

A generic third-party TCP proxy would not remove these policy decisions and often gets half-close, cancellation, error precedence, or accounting subtly wrong. The needed code is small enough to test exhaustively. No third-party TCP proxy is warranted.

### Local IPC: standard Unix sockets; `go-winio` on Windows

On macOS/Linux, [`net.ListenUnix`](https://pkg.go.dev/net#ListenUnix) and `net.Dialer` support Unix-domain streams. Put the socket in a user-private `0700` runtime directory, reject unsafe ownership/permissions, remove stale sockets only after proving no live manager owns them, and set the socket to `0600`. This lifecycle/security logic remains custom.

The Go standard library does not expose Windows named pipes as `net.Listener`. Microsoft's [`go-winio`](https://github.com/microsoft/go-winio) supplies `ListenPipe`, `DialPipeContext`, and a `net.Conn` transport. Its implementation uses I/O completion ports rather than tying up OS threads, requires Windows Vista or newer, and supports an SDDL `SecurityDescriptor` in `PipeConfig` ([source](https://github.com/microsoft/go-winio/blob/main/pipe.go)). Use byte mode for JSON Lines; its `CloseWrite` is only meaningful in message mode, which is irrelevant to request/event framing. The project is MIT licensed ([license](https://github.com/microsoft/go-winio/blob/main/LICENSE)) and depends on `x/sys`; current releases should be pinned and exercised on supported Windows versions. Configure an ACL for the current user and reject remote pipe clients rather than relying blindly on defaults.

### Process supervision

Use [`os/exec.CommandContext`](https://pkg.go.dev/os/exec#CommandContext), explicit stdout/stderr pipes, a custom `Cancel`, and nonzero `WaitDelay`. `WaitDelay` bounds hangs from a child that does not exit or inherited pipes that remain open. Do not invoke a shell: pass `ssh`, `-T`, `-D`, and the host as separate arguments, preserving system OpenSSH config/auth behavior.

**Unix:** start `ssh` in a new process group, send the group a graceful signal, wait for a bounded interval, then kill the group. `x/sys/unix` exposes `Setpgid`, `Kill`, and related primitives ([docs](https://pkg.go.dev/golang.org/x/sys/unix)). Linux `Pdeathsig` can be defense in depth but is Linux-only and is not a substitute for persisted ownership/reconciliation after manager restart. ProxyCommand and other descendants make group cleanup important.

**Windows:** create a Job Object, set `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, assign `ssh.exe`, and retain the job handle for the supervised lifetime. Microsoft documents that child processes normally join the parent's job and that closing the last handle with this flag terminates all associated processes ([Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)). `x/sys/windows` exposes `CreateJobObject`, `SetInformationJobObject`, `AssignProcessToJobObject`, and `GenerateConsoleCtrlEvent` ([docs](https://pkg.go.dev/golang.org/x/sys/windows)). Assignment immediately after ordinary `os/exec.Start` has a descendant-escape race; if testing shows this matters (for example with ProxyCommand), implement suspended creation → job assignment → thread resume using Win32 APIs rather than adding a broad supervisor framework. Also test behavior when the manager itself is already inside a restrictive job; nested jobs require Windows 8/Server 2012 or newer. `x/sys` is Go-project maintained and BSD-3-Clause licensed ([license](https://github.com/golang/sys/blob/master/LICENSE)).

No daemon framework is justified: the manager is lazy, per-user, single-instance, and already has custom reconciliation and protocol semantics. OS service integration can be a thin launchd/systemd/Windows startup layer later.

### JSONC/JWCC and semantic validation

Use [`github.com/tailscale/hujson`](https://github.com/tailscale/hujson) for `policies.jsonc`. Its documented JWCC dialect is RFC 8259 JSON plus C-style line/block comments and trailing commas. `Parse` produces an exact syntax tree whose `Pack` output is byte-for-byte identical when unchanged; comments and whitespace are represented explicitly, and RFC 6902 `Patch` can modify values while retaining unaffected syntax. `Standardize` converts input to strict JSON before decoding typed policy structures with `encoding/json`. The package is BSD-3-Clause licensed ([license](https://github.com/tailscale/hujson/blob/main/LICENSE)). Pin a release compatible with the project's minimum Go version.

Parsing is not domain validation. Reject duplicate object names, unknown fields, unsupported schema versions, invalid port ranges, duplicate policy IDs, unknown host references, unsafe bind addresses, and impossible matcher/action combinations. Implement explicit `Validate()` methods for clear domain errors rather than adding a reflection-based validator. Apply minimal AST patches and atomic writes so user comments survive CLI/UI changes; do not reformat the entire file unless explicitly requested.

### Atomic writes and file locking

For writes, keep a small project helper: create a temporary file in the destination directory with restrictive permissions, write, `Sync`, close, atomically replace/rename, then sync the parent directory on Unix where durability matters. Go supplies [`os.CreateTemp`, `File.Sync`, and `os.Rename`](https://pkg.go.dev/os), but its documentation warns that `Rename` is not necessarily atomic on non-Unix systems. Implement the Windows replacement path with `x/sys/windows` (`MoveFileEx`/`ReplaceFile`) and test power-loss and existing-destination behavior. Tiny “atomic file” packages do not eliminate these durability, permissions, or Windows semantics and are not warranted.

For the single-manager/config-write lock, implement two build-tagged adapters over `x/sys/unix.Flock` and `x/sys/windows.LockFileEx`, with context-aware retry in common code. [`gofrs/flock`](https://pkg.go.dev/github.com/gofrs/flock) is a credible BSD-3-Clause alternative with `TryLockContext`, but its own documentation warns that semantics vary by platform; because `x/sys` is already needed, the additional abstraction saves little. Treat locks as advisory coordination, not authority: socket/pipe probing and process reconciliation must handle stale lock files and crashes.

### Structured logging

Use standard [`log/slog`](https://pkg.go.dev/log/slog) with `JSONHandler` for machine logs and an optional text handler for interactive CLI diagnostics. Add stable keys such as host, listener, connection ID, process PID, operation, duration, and error. Avoid logging policy secrets, askpass responses, full environment blocks, or high-volume per-packet events. No third-party logger is needed. Go standard library code is BSD-3-Clause licensed ([Go license](https://go.dev/LICENSE)).

### JSON-RPC 2.0 over local streams

Use pinned [`github.com/creachadair/jrpc2`](https://github.com/creachadair/jrpc2) with its newline channel for Go-side JSON-RPC 2.0 validation, dispatch, request correlation, standard errors, concurrent calls, notifications, serialized writes, and cancellation plumbing. It is transport-neutral, has a stable v1 Interface, and is BSD-3-Clause licensed. Unix sockets and `go-winio` named pipes provide the byte stream; Swift implements only the product's small peer with Foundation `Codable`.

JSON-RPC does not define stream framing, version negotiation, semantic idempotency, subscriptions, backpressure, revision replay, or resynchronization. The project therefore owns `system.hello`, capability negotiation, `operation_id`, typed error data, bounded newline frames, and `manager.execute`/`snapshot`/`watch` semantics. Keep wire DTOs separate from Manager domain types and verify Go/Swift behavior with shared golden transcripts. Do **not** use standard `net/rpc/jsonrpc`: it implements JSON-RPC 1.0 and `net/rpc` is frozen ([docs](https://pkg.go.dev/net/rpc/jsonrpc)).

### Tests and performance measurements

Use standard [`testing`](https://pkg.go.dev/testing): table tests, `t.Cleanup`, parallel-safe tests, fuzzing for JSON/JSONC/protocol framing, and benchmarks for connection setup, pump throughput/allocations, many idle listeners, and policy reload. Use `net.Pipe` for protocol tests and real loopback TCP for half-close behavior (which `net.Pipe` cannot model faithfully). Add subprocess fixtures that impersonate `ssh`, scanner output, hangs, crashes, and descendant processes; run OS-specific named-pipe/job tests on Windows CI. Run `go test -race`, leak-sensitive shutdown tests, and `go test -bench -benchmem`; use Go's `benchstat` only as a developer/CI analysis tool, not a runtime dependency. Swift uses XCTest/Xcode performance tests with fixtures shared from the protocol corpus.

## Native macOS app: use Apple frameworks only

No Swift package is needed initially.

- **Launching the bundled CLI:** Foundation `Process`, `Pipe`, and `FileHandle` are the intended GUI-wrapper pattern; Apple demonstrates an app launching a helper with `Process` and paired pipes ([sample](https://developer.apple.com/documentation/security/constraining-a-tool's-launch-environment)). Resolve the executable from `Bundle`, never through a shell or `PATH`; set arguments/environment explicitly; drain output while the process runs to avoid pipe backpressure; coordinate EOF and termination rather than assuming the termination callback means all bytes were consumed. Apple's DTS guidance recommends Dispatch I/O for robust child-process pipe handling and details `Process` cancellation/pipe pitfalls ([guidance](https://developer.apple.com/forums/thread/690310)).
- **JSON Lines:** accumulate bytes from the stdout pipe, split only on LF, enforce a maximum line size, and decode each complete line with Foundation `JSONDecoder` into `Codable` envelope types. Preserve partial trailing data between reads and keep decoding off the main actor; publish typed state changes on `MainActor`.
- **Menu bar:** SwiftUI [`MenuBarExtra`](https://developer.apple.com/documentation/swiftui/menubarextra) is the standard persistent menu-bar scene; `.menuBarExtraStyle(.window)` supports richer content. Apple notes that a menu-only utility can use `LSUIElement`, and that removing a standalone extra may terminate the app ([WWDC22](https://developer.apple.com/videos/play/wwdc2022/10061/)). This sets a macOS 13 deployment floor; use AppKit `NSStatusItem` only if older macOS support becomes necessary.
- **Ephemeral askpass:** provide a small bundled, signed askpass executable/mode that receives OpenSSH's prompt argument, presents a SwiftUI `SecureField` (or AppKit secure text field), writes only the answer plus newline to stdout, and exits immediately. Launch `ssh` with no TTY and a minimal environment containing `SSH_ASKPASS=<absolute bundled path>` and `SSH_ASKPASS_REQUIRE=force`; upstream `ssh(1)` specifies that `force` uses askpass regardless of `DISPLAY`, while the traditional path requires no terminal plus `DISPLAY` and `SSH_ASKPASS` ([OpenBSD `ssh(1)`](https://man.openbsd.org/ssh.1#SSH_ASKPASS)). Never persist, log, cache, or send the response over manager IPC. Serialize prompts and handle cancellation/SSH retry cleanly. System OpenSSH remains responsible for deciding when and what to prompt.

SwiftUI/Foundation are OS SDK frameworks rather than redistributed third-party libraries; their use is governed by Apple's SDK terms. Open-source Swift itself uses Apache-2.0 with a runtime exception ([license](https://github.com/swiftlang/swift/blob/main/LICENSE.txt)), but that is not a license to redistribute Apple framework implementations.

## Components not to reuse

- **`golang.org/x/crypto/ssh`: avoid.** It is a capable BSD-3-Clause SSH client/server implementation ([docs](https://pkg.go.dev/golang.org/x/crypto/ssh), [license](https://github.com/golang/crypto/blob/master/LICENSE)), but using it would make this project own algorithm policy, authentication callbacks, agent/key parsing, host-key verification, config compatibility, ProxyCommand/jump-host behavior, and prompt UX. Its API explicitly requires authentication methods and a `HostKeyCallback`. That conflicts with the settled rule that system OpenSSH owns config/auth/known-host behavior.
- **Full tunnel projects: avoid importing or forking.** For example, MIT-licensed [Chisel](https://github.com/jpillora/chisel) is an HTTP/WebSocket tunnel secured by embedded `crypto/ssh`, with its own server, keys, authentication, reconnect, SOCKS server, reverse forwarding, and CLI. Those are precisely the layers this architecture does not need. Other VPN-style tools commonly require packet interception, remote installation, privileges, or copyleft obligations. They may be useful behavioral references, not dependencies.
- **VS Code source: do not reuse for the tunnel.** The core [VS Code repository](https://github.com/microsoft/vscode) is MIT licensed, but Remote-SSH installs a VS Code Server and solves remote editor extension execution, not policy-driven local forwarding ([official architecture](https://code.visualstudio.com/docs/remote/ssh)). More importantly, Microsoft's Remote Development extension source is not published as reusable MIT code and its product license restricts reverse engineering and integrated redistribution ([license](https://aka.ms/vscode-remote/license)). Learn from observable UX only; do not copy proprietary/minified implementation.
- **Embedded OpenSSH: avoid.** OpenSSH portable has a multi-part permissive license with BSD-style and other notices ([license](https://github.com/openssh/openssh-portable/blob/master/LICENCE)), but embedding it would introduce a large native security-critical codebase, platform build/patch burden, duplicate user configuration, and credential handling. Executing the installed `ssh` is the intended seam and preserves vendor security updates.
- **Third-party TCP proxy, heavy RPC, daemon, logger, validator, atomic-file, and supervisor frameworks: avoid now.** Beyond the selected narrow `jrpc2` dependency, each either wraps a small standard-library surface or fails to remove the domain-specific lifecycle and compatibility work.

## 1. Recommended dependencies now

| Dependency | Use | License | Caveat |
|---|---|---|---|
| Go standard library (`flag`, `context`, `os/exec`, `net`, `io`, `encoding/json`, `log/slog`, `testing`) | CLI, process baseline, sockets/TCP, proxy pump, protocol, logs, tests | BSD-3-Clause | Windows named pipes/jobs and JSONC syntax preservation are not covered |
| `golang.org/x/net/proxy` (candidate) | SOCKS5 CONNECT to loopback `ssh -D` | BSD-3-Clause | Direct APIs do not simultaneously expose cancellable CONNECT and TCP `CloseWrite`; retain only behind the tested Dial Adapter |
| `github.com/tailscale/hujson` | Parse, preserve, patch, and format JSONC/JWCC | BSD-3-Clause | Typed decode and semantic validation still use project code plus `encoding/json`; pin a Go-compatible release |
| `golang.org/x/sys` | Unix/Windows locks and process/job primitives | BSD-3-Clause | Build-tag OS code; pin/update with Go toolchain |
| `github.com/Microsoft/go-winio` (Windows build only, when Windows starts) | Named pipes as `net.Listener`/`net.Conn` | MIT | Windows Vista+; configure SDDL; test version-specific behavior |
| `github.com/creachadair/jrpc2` | Go-side JSON-RPC 2.0 machinery and newline channels | BSD-3-Clause | Product versioning, idempotency, Watch semantics, backpressure, and Swift peer remain project-owned |
| SwiftUI, Foundation, Dispatch, XCTest | Native menu UI, process/pipes, Codable JSON-RPC, askpass, tests | Apple SDK terms | `MenuBarExtra` implies macOS 13+ |

Pin direct Go modules, commit checksums, and let package imports—not broad helper frameworks—determine transitive code in the binary.

## 2. Defer or avoid

- **Defer:** Cobra until the CLI demonstrably needs a deep command tree/completion generation; `gofrs/flock` until custom two-platform locking proves burdensome; AppKit `NSStatusItem` only for pre-macOS-13 support; a higher-level Windows job wrapper only if suspended launch cannot be implemented reliably with `x/sys`.
- **Avoid:** `x/crypto/ssh`, embedded OpenSSH, Chisel/sshuttle/full-tunnel code, VS Code Remote-SSH code, generic TCP proxy packages, standard `net/rpc/jsonrpc`, gRPC/Connect/plugin frameworks, daemon/service frameworks, third-party structured loggers, reflection validators, and tiny atomic-write wrappers.

## 3. Genuinely custom code still required

1. Policy domain types, defaults, semantic validation, migrations, and diagnostics.
2. JSON-RPC method/wire schemas, compatibility rules, frame limits, authorization, semantic idempotency, Watch revisions/backpressure/resync, typed error data, and Swift/Go conformance fixtures.
3. Lazy single-manager discovery/startup and secure Unix-socket/named-pipe lifecycle.
4. Development Host identity, desired/observed state reconciliation, scanner launch/stream parser, stale-state handling, retries/backoff, and health model.
5. Exact `ssh -T -D` command/environment construction, readiness detection, port allocation, process-group/job supervision, restart policy, and cleanup after crashes.
6. Preferred-port allocation, bounded fallback, listener ownership, and collision policy.
7. Cancellation-safe, half-close-correct TCP pump plus metrics and error classification.
8. Durable policy/state write semantics and advisory lock wrapper across Unix/Windows.
9. macOS state model, menu-bar UX, CLI lifecycle/JSONL reader, and secure one-shot askpass helper.
10. Cross-platform integration tests, fake SSH/scanner fixtures, fault injection, idle-footprint measurements, throughput benchmarks, packaging/signing, and upgrade tests.
