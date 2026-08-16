# Implementation sequence

## Risk spike

The completed throwaway spike validated one system `ssh -T -D … sh -s` connection carrying both scanner observations and SOCKS-proxied traffic. A disposable Ubuntu SSH container—not a configured Development Host—provided the remote environment. Dynamic Local Endpoint add/remove, dual-stack allocation, TCP half-close, disconnect/reconnect behavior, and measured transport budgets passed. The [transport spike verdict](./transport-spike-verdict.md) records the evidence and implementation constraints; the spike code and branch were discarded.

## Vertical slices

Slices 1–4 are complete: the baseline, control-plane tracer, one-host Manual Forward path, and agentless Discovery/Snapshot Watch path are implemented. One Forwarding Session now carries the fixed versioned scanner plus SOCKS traffic; the Manager publishes canonical complete Snapshots with Discovery evidence, and capability-negotiated JSON-RPC clients can Watch them through bounded latest-value streams. The architecture pass opened the per-host actor seam behind core.Manager (session, Discovery, and reconnect under one actor lock, publishing host state to the Manager) and added the Listener Lifetime tracker behind it, publishing per-listener verdicts on the wire, so Policy reconciliation lands on a prepared continuity seam. Policy reconciliation (Forwarding Policies, One-time Approval and Suppression) remains the next slice.

1. Initialize the new local repository and preserve the accepted design baseline.
2. Establish the Go module, disposable Linux integration harness, Manager Interface, and JSON-RPC hello/status path.
3. Deliver one-host Manual Forward end to end.
4. Add agentless discovery and Snapshot Watch.
5. Add Policy, Listener Lifetime, and continuous reconciliation. Slice 5 resolves the two named decisions (recorded by the round-14 architecture review) as follows. (a) Wall clock: the "two consecutive observations and five seconds" removal bound (discovery-and-policy.md) is implemented with both conditions ANDed — observation cycles come from the actor's fact stream, and the five-second floor comes through a new injected clock seam (managerOptions.now, defaulting to time.Now, alongside the existing retryDelay/retryWait injection pattern). The Listener Lifetime tracker stays deliberately clock-free (lifetime.go) — wall time reaches only the reconciliation path, never verdicts. Tests inject the clock exactly like the retry seams. (b) Session outage: an outage freezes the tracker (it advances only on ObservationSets) and does NOT count toward disappearance grace — per the spec's own reading, "connection loss and scanner failure do not count as disappearance; reconnection obtains a complete observation before cleanup resumes" (discovery-and-policy.md). "Listener ended" (or replaced) retires One-time Approvals; an outage never does. Both readings are pinned by tests in this slice.
6. Complete the domain-oriented CLI.
7. Build the SwiftUI menu panel and Dashboard.
8. Add packaging, signing, notarization, and release checks.

Each formal slice starts with one failing behavior test at an agreed Seam, adds only enough Implementation to pass, and then advances to the next behavior. The installed legacy utility remains untouched throughout.
