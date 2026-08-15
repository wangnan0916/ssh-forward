# Implementation sequence

## Risk spike

The completed throwaway spike validated one system `ssh -T -D … sh -s` connection carrying both scanner observations and SOCKS-proxied traffic. A disposable Ubuntu SSH container—not a configured Development Host—provided the remote environment. Dynamic Local Endpoint add/remove, dual-stack allocation, TCP half-close, disconnect/reconnect behavior, and measured transport budgets passed. The [transport spike verdict](./transport-spike-verdict.md) records the evidence and implementation constraints; the spike code and branch were discarded.

## Vertical slices

Slices 1–3 are now complete: the baseline, control-plane tracer, and one-host Manual Forward path are implemented. The transport currently sends a fixed inert `sh -s` program solely to keep the dedicated SSH/SOCKS session alive; slice 4 replaces it with the fixed, versioned observation scanner and wires framed stdout. Discovery remains the next slice.

1. Initialize the new local repository and preserve the accepted design baseline.
2. Establish the Go module, disposable Linux integration harness, Manager Interface, and JSON-RPC hello/status path.
3. Deliver one-host Manual Forward end to end.
4. Add agentless discovery and Snapshot Watch.
5. Add Policy, Listener Lifetime, and continuous reconciliation.
6. Complete the domain-oriented CLI.
7. Build the SwiftUI menu panel and Dashboard.
8. Add packaging, signing, notarization, and release checks.

Each formal slice starts with one failing behavior test at an agreed Seam, adds only enough Implementation to pass, and then advances to the next behavior. The installed legacy utility remains untouched throughout.
