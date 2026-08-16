# Core Manager Module

## External Interface

The central `core.Manager` is a deep Module. Its Interface is the shared test surface and contains no JSON, IPC, OpenSSH, filesystem, socket, or Swift details.

```go
type Manager interface {
    Execute(context.Context, Command) (Outcome, error)
    Snapshot(context.Context) (Snapshot, error)
    Watch(context.Context) (SnapshotStream, error)
    Close(context.Context) error
}

type SnapshotStream interface {
    Next(context.Context) (Snapshot, error)
    Close() error
}
```

`Command` is a sealed set of domain actions. Each command carries a manager-lifetime `CommandID` for idempotent retries and may carry an expected `Revision` when protecting interactive edits. Repeating an identical `CommandID` returns its original Outcome without a new revision; reuse for different input returns `command_id_conflict`. The initial concrete commands add a loopback-only Manual Forward and remove a Forward by ID. IPC versioning and malformed JSON are Adapter concerns, not Manager errors.

`Snapshot` is an immutable, deterministically ordered view at one manager-wide, monotonically increasing `Revision`; a newly constructed Manager begins at revision `0` before any caller-visible transition. It carries one `Host` (the Manager's single Development Host; nil while none is configured), so the Interface exposes only what the Implementation delivers — multi-host generality lives in the IPC wire format, not here. `ListenerLifetimes` reports each Remote Listener's Listener Lifetime status (new, continuous, replaced, grace, ended) and is currently consumed by core tests; the IPC Adapter does not yet marshal it, pending Policy reconciliation's wire design. The first `SnapshotStream.Next` returns the complete immutable Snapshot captured at the subscription point; later calls return complete latest-value Snapshots only after caller-visible changes, so revisions may jump. A slow watcher never blocks publication and does not require resync merely because revisions were coalesced. The Manager admits at most 128 streams, permits one in-flight `Next` per stream, treats cancellation as cancellation of only that wait, and makes stream `Close` idempotent.

`Execute` is concurrency-safe and linearizable. Persistent intent commands return after an atomic configuration write. Add-Forward commands return after the Local Endpoint is allocated; remove commands stop the Endpoint before normal return. Potentially blocking socket lifecycle work runs outside the Manager state lock. Cancellation before commit leaves no state; for removal, commit begins when the command exclusively reserves the Forward for shutdown. Cancellation after commit stops waiting but does not imply rollback: Endpoint closure and state publication finish in the background, and identical retries wait for that completion. Committed state is recoverable through Snapshot or an idempotent retry.

## Hidden Implementation

The current Implementation builds one canonical immutable Snapshot under the Manager lock for every caller-visible transition and publishes it to bounded latest-value subscriptions. A hidden Forward table gives each Forward sole ownership of its Local Endpoint lifecycle and stable projection. A per-host actor (one per Manager, since one Manager serves one user and one host) owns the Forwarding Session, its data-path holder, Discovery ingestion, observation continuity, and reconnect scheduling behind its own lock; it publishes the published host shape to the Manager (the Manager adds only the Forward table at publication time), and command and Snapshot state live under the Manager lock. Connection start is a Manager declaration: a command that needs transport marks the host Connecting under the Manager lock and publishes it together with the command outcome, then unconditionally arms the actor with a single re-armable lifecycle entry. Arming is race-free: the actor re-checks liveness under its own lock, so a command racing the actor's terminal publication (which clears liveness before publishing Disconnected) re-arms once that publication lands — the Manager never accepts a command it cannot attempt. Each connect loop closes only the liveness channel it was launched with, so re-arming can never double-close or wake a stale wait. The actor's connect loop runs at most once per arming; a terminal session failure (for example an SSH authentication or host-key rejection) ends the loop, and a later command that needs transport re-arms it. Per-host publication is deduplicated at one point in the actor: no-change states never advance the Manager revision, while the Listener Lifetime tracker always advances. Blocking socket work (Forward allocation and closure) runs outside both locks. A Listener Lifetime tracker inside the actor classifies every observation generation (new, continuous, replaced, grace, ended) — always advancing, so absent listeners pass through grace to ended even while the observation set itself is unchanged — giving Policy reconciliation a continuity seam when it lands.

The Implementation hides system `ssh -G` and `ssh -T -D`, scanner validation, SOCKS readiness, dual-stack local allocation, policy evidence, disappearance grace, reconnect jitter, JSONC persistence, sensitive-data redaction, and idle shutdown.

## Internal Seams

- **OpenSSH Seam — true external:** `Connect` returns a Forwarding Session whose narrow Interface is validated `Next`, constrained `Dial`, and bounded idempotent `Close`. Production and scripted Adapters own alias validation, SSH/SOCKS lifecycle, scanner framing, observation validation, and typed terminal disposition.
- **Clock/random Seam — in-process controllability:** real and deterministic Adapters drive reconnect and disappearance tests.
- **Configuration — local-substitutable:** tests use temporary directories and the real JSONC implementation; no filesystem Interface leaks through the external Seam.
- **Loopback networking — local-substitutable:** tests use real loopback sockets except narrowly scripted allocation failures where an internal Adapter earns its keep.
- **IPC — external Adapter:** Unix sockets/named pipes and `creachadair/jrpc2` translate project-owned JSON-RPC methods into Commands, Outcomes, and Snapshots; wire DTOs remain separate from domain types.

Tests exercise behavior through the Manager Interface. Separate Adapter tests verify OpenSSH argv/environment/exit classification and IPC framing; they do not duplicate Manager reconciliation tests.

## Rejected shapes

- Exposing only an actor `Dispatch(Message)` minimized method count but moved the semantic Interface into a large message union and reduced caller discoverability.
- Resource handles for hosts, policies, forwards, and state produced a wide, shallow Interface and encouraged IPC to mirror internal resources.
- Fine-grained domain events would force every client to implement a reducer. Complete coalesced Snapshots provide greater caller Leverage until measured payload size proves otherwise.
