# Transport spike verdict

## Decision

Proceed with [ADR 0004](../adr/0004-proxy-through-system-ssh-dynamic-forwarding.md): one system `ssh -T -D` process per connected Development Host can carry the agentless observation stream while Go-owned Local Endpoints proxy dynamic traffic through its SOCKS listener. The architecture met the functional and measured transport budgets in the disposable test topology.

The spike code remains throwaway. Only this verdict, the resulting constraints, and updated design decisions belong on `main`.

## Isolation and environment

All final evidence used a disposable Ubuntu 24.04 container, not a configured Development Host. The harness used a real OpenSSH server, an unprivileged Linux user, real `/proc` and `ss`, IPv4 and IPv6 loopback listeners, an isolated SSH config, per-run client and host keys, and a random host-loopback SSH port. It rejected SSH host/config overrides and never read the user's default SSH configuration.

Reference environment:

- MacBook Pro `MacBookPro18,3`, Apple M1 Pro, 32 GiB
- macOS Tahoe 26.6.1 (`25G76`), arm64
- Go 1.26.6, darwin/arm64
- Apple system OpenSSH 10.3p1 with LibreSSL 3.3.6
- Docker Engine 29.4.0 through OrbStack, aarch64
- Ubuntu 24.04 OCI index pinned at `sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea`

## Functional result

| Behavior | Result |
|---|---|
| One `ssh -T -D … sh -s` carries scanner stdout and SOCKS traffic | Passed |
| TCP response after client `CloseWrite` | Passed |
| Add and remove Local Endpoints without restarting SSH | Passed |
| Removed Endpoint stops accepting before replacement | Passed |
| Same Allocated Local Port on `127.0.0.1` and `::1` | Passed |
| SOCKS connection to remote IPv4 and IPv6 loopback | Passed |
| Scanner continues while Endpoints change | Passed |
| Process ownership visible to the unprivileged SSH user | Passed |
| Established connection fails promptly when SSH stops | Passed |
| New connection through retained Endpoint fails promptly while disconnected | Passed |
| Same retained Endpoint works after swapping in a reconnected transport | Passed |
| Fixture, scanner, SSH children, container, network, image tag, and keys clean up | Passed |

The final controlled run repeated the complete scenario five times. Every run passed all functional assertions and post-run leak checks.

## Performance result

Each controlled run used nine interleaved small exchanges and five interleaved 16 MiB round trips through direct SOCKS, direct `ssh -L`, and the Go Local Endpoint path. Interleaving rotated path order to reduce drift. Both the direct-SOCKS and Go-proxy latency clocks included SOCKS CONNECT. Absolute rates describe the local Docker topology only; the paired ratios are the relevant architecture evidence.

| Budget | Result | Status |
|---|---:|---|
| Manager plus product SSH idle RSS ≤ 40 MiB | 10.53 MiB median and maximum over 48.97 s | Passed provisionally |
| Combined idle CPU ≤ 0.5% | No 0.01 s process-time tick observed; conservative upper bound 0.041% | Passed provisionally |
| New listener discovery p95 ≤ 3 s | 1.876 s conservative p95/max | Passed |
| Forward throughput ≥ 90% of direct `ssh -L` | 99.49% median; 98.71–101.26% range | Passed |
| Added local connection latency ≤ 2 ms median | 0.142 ms median; 0.419 ms maximum | Passed |
| Remote scanner average CPU ≤ 0.5% | 0.0265% stable two-second fingerprint estimate | Passed provisionally |
| Desktop stack idle RSS ≤ 80 MiB | Not measurable before the desktop exists | Deferred |
| Warm CLI p95 ≤ 50 ms | Not measurable before the Manager/CLI exists | Deferred |

Additional observed medians were 0.790 ms for an established connection to fail after SSH termination, 0.458 ms for a new disconnected connection to fail, 139.5 ms for the replacement SSH transport to become ready, and 1.833 ms for the first exchange through the retained Endpoint after reconnect.

The idle sample excludes the fixture-control SSH process because it is test infrastructure, but includes the spike manager and the product-path `ssh -D` child. The scanner estimate came from 2,000 stable `/proc` fingerprints; changed-topology attribution through `ss` cost about 0.55 ms CPU per event in the container. Full-product resource gates must be rerun after production discovery, policy, IPC, CLI, and desktop code exists.

## Findings that constrain implementation

1. **Do not set `ClearAllForwardings=yes` on the transport command.** An A/B test showed that it removes command-line `-D` and `-L` listeners as well as configuration-file forwards. Isolation instead uses a dedicated process with `ControlMaster=no`, `ControlPath=none`, explicit loopback addresses, and `ExitOnForwardFailure=yes`.
2. **The SOCKS Dial Adapter must provide cancellation and half-close together.** In the tested `golang.org/x/net/proxy` v0.58.0 API, legacy `Dial` preserves the raw TCP connection and `CloseWrite` but does not bound the SOCKS handshake; `DialContext` bounds establishment but returns a wrapper whose public method set does not expose the underlying TCP `CloseWrite`. Production selection remains behind tests for bounded cancellation, malformed responses, IPv4/IPv6 CONNECT, and response-after-client-EOF. The spike's small RFC 1928 dialer proves feasibility but is not automatically production code.
3. **Endpoint shutdown must cancel an in-progress SOCKS handshake.** The Endpoint owns a cancellation context, tracks accepted and remote connections, and applies a finite handshake deadline before waiting for goroutines.
4. **Half-close needs a bounded drain.** Normal EOF propagates with `CloseWrite` in both directions, while a finite post-half-close drain prevents a client that ignores upstream EOF from retaining a goroutine indefinitely.
5. **Reconnection swaps transport, not listener allocation.** A retained Endpoint consults a concurrency-safe current Dial Adapter for each accepted connection. Existing connections fail on transport loss; future connections use the replacement transport without rebinding the local port.
6. **Discovery should be two-stage.** A cheap `/proc/net/tcp*` listener fingerprint runs every two seconds. Process attribution runs on fingerprint changes, on bounded backoff for missing metadata, and on a slower periodic refresh to catch `exec` or ancestry changes without paying full `ss`/`lsof` cost every cadence.
7. **Remote test services must not share the scanner lifecycle.** The fixture is independent test infrastructure so stopping the product-path SSH session accurately models a development service that remains alive across reconnect.
8. **Container integration is the default safety boundary.** Local and CI tests use the same pinned image, local Docker daemon, random loopback publication, isolated keys, run-scoped project/image names, and failure-visible cleanup. Automated tests never resolve a configured Development Host.

## Limitations

Docker supplies real Linux and OpenSSH behavior but not a physical host's WAN latency, packet loss, MTU, VPN, ProxyJump, enterprise SSH policy, systemd, security modules, arbitrary kernel configuration, or real editor process trees. The throughput result therefore validates relative proxy overhead, not a public absolute-throughput claim. Full process ancestry, permission degradation, policy reconciliation, sleep/wake behavior, CLI latency, desktop resources, and packaging remain formal implementation work.
