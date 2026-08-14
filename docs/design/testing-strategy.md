# Testing strategy

Formal implementation uses red-green TDD in vertical slices. Tests exercise behavior only through pre-agreed Seams:

1. `core.Manager` — `Execute`, `Snapshot`, `Watch`, and `Close` behavior, using private scripted OpenSSH/time Adapters.
2. OpenSSH Adapter — argv/environment construction, scanner/SOCKS lifecycle, exit classification, and process cleanup.
3. JSON-RPC Adapter — shared Go/Swift golden transcripts, framing bounds, version mismatch, typed errors, cancellation, Watch coalescing, and resync.
4. CLI — subprocess behavior and structured output against a real local Manager.
5. Proxy — real loopback half-close, cancellation, throughput, allocation, and containerized remote end-to-end tests.
6. Swift Manager client and Dashboard state model — protocol fixtures and observable UI state transitions.

Tests do not address actor mailboxes, private matcher helpers, internal fields, or implementation call counts. Manager tests assert complete observable outcomes and survive changes to actor layout or Adapter wiring. Race, fuzz, leak, fault-injection, and benchmark runs supplement—not replace—behavioral slices.

## Local and CI layers

Fast Go behavior, Adapter, IPC, CLI, and loopback tests run without Docker. Swift and Darwin-specific tests run on macOS. A disposable, pinned Ubuntu test image supplies a real unprivileged Linux user, OpenSSH server, `/proc`, `ss`, `lsof`, and fixture process trees for integration tests. The image publishes SSH only on a random host-loopback port and uses an isolated ephemeral key, SSH config, and known-hosts file.

The same image is used by local integration runs and Linux CI through Docker Engine 28 or newer on a local Unix socket; remote Docker daemons are rejected because loopback publication and bind-mount paths would refer to the wrong machine. Linux CI exercises real scanner, dynamic forwarding, SOCKS, half-close, disconnect, reconnect, and cleanup behavior. macOS CI exercises Darwin-specific core and Swift behavior through scripted OpenSSH Adapters; the developer's Mac plus the container covers the exact macOS-system-OpenSSH-to-Linux combination. Automated tests never resolve or connect to a configured Development Host. A real host may be used only in a separately invoked, explicit manual test authorized by the user.

Shared CI runners record performance trends but do not enforce absolute CPU, RSS, latency, or throughput budgets. Those budgets are gated on the reference Tahoe Mac against direct `ssh -L` in the same container topology.
