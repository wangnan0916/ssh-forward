# Implementation sequence

## Risk spike

The completed throwaway spike validated one system `ssh -T -D … sh -s` connection carrying both scanner observations and SOCKS-proxied traffic. A disposable Ubuntu SSH container—not a configured Development Host—provided the remote environment. Dynamic Local Endpoint add/remove, dual-stack allocation, TCP half-close, disconnect/reconnect behavior, and measured transport budgets passed. The [transport spike verdict](./transport-spike-verdict.md) records the evidence and implementation constraints; the spike code and branch were discarded.

## Vertical slices

Slices 1–6 are complete. One Forwarding Session carries the fixed versioned scanner plus SOCKS traffic. The Manager publishes canonical complete Snapshots with Discovery evidence, Listener Lifetime verdicts, and policy-reconciled Managed Forwards. Capability-negotiated JSON-RPC clients can Watch those Snapshots. The CLI is the public command surface: it auto-spawns a per-user manager and speaks the v1 wire.

Slice 5 resolved two named decisions. (a) Wall clock: the "two consecutive observations and five seconds" removal bound (discovery-and-policy.md) is implemented with both conditions ANDed — observation cycles come from the actor's fact stream, and the five-second floor comes through an injected clock seam (`managerOptions.now`, defaulting to `time.Now`). The Listener Lifetime tracker stays clock-free (`lifetime.go`) — wall time reaches only the reconciliation path, never verdicts. (b) Session outage: an outage freezes the tracker (it advances only on ObservationSets) and does not count toward disappearance grace. Both readings are pinned by tests.

1. Initialize the new local repository and preserve the accepted design baseline.
2. Establish the Go module, disposable Linux integration harness, Manager Interface, and JSON-RPC hello/status path.
3. Deliver one-host forwarding end to end.
4. Add agentless discovery and Snapshot Watch.
5. Add Policy, Listener Lifetime, and continuous reconciliation.
6. Complete the domain-oriented CLI.
7. Build the TUI (after the CLI is ready).
8. Build the macOS desktop (after the TUI is ready), then packaging, signing, notarization, and release checks.

Each formal slice starts with one failing behavior test at an agreed Seam, adds only enough Implementation to pass, and then advances to the next behavior.
