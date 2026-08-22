# Implementation sequence

## Risk spike

The completed throwaway spike validated one system `ssh -T -D … sh -s` connection carrying both scanner observations and SOCKS-proxied traffic. A disposable Ubuntu SSH container—not a configured Development Host—provided the remote environment. Dynamic Local Endpoint add/remove, dual-stack allocation, TCP half-close, disconnect/reconnect behavior, and measured transport budgets passed. The [transport spike verdict](./transport-spike-verdict.md) records the evidence and implementation constraints; the spike code and branch were discarded.

## Vertical slices

Slices 1–6 are complete. One Forwarding Session carries the fixed versioned scanner plus SOCKS traffic. The Manager publishes canonical complete Snapshots with Discovery evidence and policy-reconciled Managed Forwards. A small versioned JSON-RPC Adapter lets clients Snapshot and Watch that state. The CLI is the public command surface: it auto-spawns a per-user manager and speaks the v1 wire. The loopback WebUI was removed from this tree.

Slice 5's observation-path hysteresis is two consecutive observations for create and disappearance. A saved policy edit applies immediately and resets that hysteresis. Session outage does not count as disappearance: the observation wake advances only on ObservationSets.

1. Initialize the new local repository and preserve the accepted design baseline.
2. Establish the Go module, disposable Linux integration harness, Manager Interface, and JSON-RPC hello/status path.
3. Deliver one-host forwarding end to end.
4. Add agentless discovery and Snapshot Watch.
5. Add Policy and continuous reconciliation.
6. Complete the domain-oriented CLI.
7. ~~Build the loopback WebUI.~~ Removed from this tree.
8. Build the macOS desktop, then packaging, signing, notarization, and release checks.

Each formal slice starts with one failing behavior test at an agreed Seam, adds only enough Implementation to pass, and then advances to the next behavior.
