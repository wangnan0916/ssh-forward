# Core Manager Module

## External Interface

The central `core.Manager` is a deep Module. Its Interface is the shared test surface and contains no JSON, IPC, OpenSSH, filesystem, socket, or Swift details.

```go
type Manager interface {
    Snapshot(context.Context) (Snapshot, error)
    Watch(context.Context) (SnapshotStream, error)
    Close(context.Context) error
}

type SnapshotStream interface {
    Next(context.Context) (Snapshot, error)
    Close() error
}
```

Persistent intent (Forwarding Policies, default host) lives in versioned JSONC files, not on this Interface. IPC versioning and malformed JSON are Adapter concerns, not Manager errors.

`Snapshot` is an immutable, deterministically ordered view at one manager-wide, monotonically increasing `Revision`; a newly constructed Manager with no host begins at revision `0`. It carries one `Host` (the Manager's single Development Host; nil while none is configured). Policy match evidence (which policy fired, and why) is not on the Snapshot. The first `SnapshotStream.Next` returns the complete immutable Snapshot captured at the subscription point; later calls return complete latest-value Snapshots only after caller-visible changes, so revisions may jump. A slow watcher never blocks publication and does not require resync merely because revisions were coalesced. The Manager admits at most 128 streams, permits one in-flight `Next` per stream, treats cancellation as cancellation of only that wait, and makes stream `Close` idempotent.

A Manager constructed with a host and connector starts the Forwarding Session immediately: it publishes Connecting and arms the host actor so discovery and Auto-forward run without a separate command. A permanent SSH failure leaves the host Disconnected until the Manager is restarted.

## Hidden Implementation

The current Implementation builds one canonical immutable Snapshot under the Manager lock for every caller-visible transition and publishes it to bounded latest-value subscriptions. A hidden Forward table gives each Forward sole ownership of its Local Endpoint lifecycle and stable projection. A per-host actor (one per Manager, since one Manager serves one user and one host) owns the Forwarding Session, its data-path holder, Discovery ingestion, observation continuity, and reconnect scheduling behind its own lock; it publishes the published host shape to the Manager (the Manager adds only the Forward table at publication time). Connection start is a Manager declaration at construction: it marks the host Connecting under the Manager lock and publishes it, then arms the actor. The actor's connect loop runs at most once per arming; a terminal session failure (for example an SSH authentication or host-key rejection) ends the loop. Per-host publication is deduplicated at one point in the actor: no-change states never advance the Manager revision. Blocking socket work (Forward allocation and closure) runs outside both locks. Policy reconciliation evaluates every observation generation: a Managed Forward is created after two consecutive Auto-forward matches and removed after two consecutive observations that no longer match.

The Implementation hides system `ssh -G` and `ssh -T -D`, scanner validation, SOCKS readiness, dual-stack local allocation, policy evaluation, and reconnect jitter. Sensitive-data redaction in logs and idle shutdown are specified but not yet implemented.

## Internal Seams

- **OpenSSH Seam — true external:** `Connect` returns a Forwarding Session whose narrow Interface is validated `Next`, constrained `Dial`, and bounded idempotent `Close`. Production and scripted Adapters own alias validation, SSH/SOCKS lifecycle, scanner framing, observation validation, and typed terminal disposition.
- **Clock/random Seam — in-process controllability:** real and deterministic Adapters drive reconnect tests.
- **Configuration — local-substitutable:** tests use temporary directories and the real JSONC implementation; no filesystem Interface leaks through the external Seam.
- **Loopback networking — local-substitutable:** tests use real loopback sockets except narrowly scripted allocation failures where an internal Adapter earns its keep.
- **IPC — external Adapter:** Unix sockets/named pipes and `creachadair/jrpc2` translate project-owned JSON-RPC methods into Snapshots; wire DTOs remain separate from domain types.

Tests exercise behavior through the Manager Interface. Separate Adapter tests verify OpenSSH argv/environment/exit classification and IPC framing; they do not duplicate Manager reconciliation tests.

## Rejected shapes

- Exposing only an actor `Dispatch(Message)` minimized method count but moved the semantic Interface into a large message union and reduced caller discoverability.
- Resource handles for hosts, policies, forwards, and state produced a wide, shallow Interface and encouraged IPC to mirror internal resources.
- Fine-grained domain events would force every client to implement a reducer. Complete coalesced Snapshots provide greater caller Leverage until measured payload size proves otherwise.
