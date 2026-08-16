# Testing strategy

Formal implementation uses red-green TDD in vertical slices. Tests exercise behavior only through pre-agreed Seams:

1. `core.Manager` — `Execute`, `Snapshot`, `Watch`, and `Close` behavior, using private scripted OpenSSH/time Adapters.
2. OpenSSH Adapter — argv/environment construction, scanner/SOCKS lifecycle, exit classification, and process cleanup.
3. JSON-RPC Adapter — shared Go/Swift golden transcripts, framing bounds, version mismatch, typed errors, cancellation, Watch coalescing, and resync.
4. CLI — subprocess behavior and structured output against a real local Manager.
5. Proxy — real loopback half-close, cancellation, throughput, allocation, and containerized remote end-to-end tests.
6. Swift Manager client and Dashboard state model — protocol fixtures and observable UI state transitions.

Tests do not address actor mailboxes, private matcher helpers, or internal fields. Manager tests drive the per-host actor deterministically through the declared `managerOptions` test seam — connector and publisher injection — and deliberately pin specific publication counts (a command outcome and its Connecting state share a revision; no-change snapshots are deduplicated; both are exactly the behaviors that drift). Those pins are the intended update points when slice 5 adds publication sites; relax the count, not the monotone progression. Other packages stay black-box through their module interfaces. Race, fuzz, leak, fault-injection, and benchmark runs supplement—not replace—behavioral slices.

## Local and CI layers

Fast Go behavior, Adapter, IPC, CLI, and loopback tests run without Docker. Swift and Darwin-specific tests run on macOS. A disposable, pinned Ubuntu test image supplies a real unprivileged Linux user, OpenSSH server, `/proc`, `ss`, `lsof`, and fixture process trees for integration tests. The image publishes SSH only on a random host-loopback port and uses an isolated ephemeral key, SSH config, and known-hosts file.

The same image is used by local integration runs and Linux CI through Docker Engine 28 or newer on a local Unix socket; remote Docker daemons are rejected because loopback publication and bind-mount paths would refer to the wrong machine. Its entrypoint owns independent unprivileged IPv4 and IPv6 half-close responders, so product SSH reconnect or shutdown never controls the simulated development workload. Linux CI exercises real scanner, dynamic forwarding, SOCKS, half-close, disconnect, reconnect, and cleanup behavior. macOS CI exercises Darwin-specific core and Swift behavior through scripted OpenSSH Adapters; the developer's Mac plus the container covers the exact macOS-system-OpenSSH-to-Linux combination. Automated tests never resolve or connect to a configured Development Host. A real host may be used only in a separately invoked, explicit manual test authorized by the user.

Fast local and macOS-CI checks run `go test ./...` and `go test -race ./...` from `cli/`. Local development and Linux CI invoke the same repository-level `./scripts/test-integration` command for the disposable SSH host.

Shared CI runners record performance trends but do not enforce absolute CPU, RSS, latency, or throughput budgets. Those budgets are gated on the reference Tahoe Mac against direct `ssh -L` in the same container topology.
