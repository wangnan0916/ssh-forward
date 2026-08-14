# Core Manager Module

## External Interface

The central `core.Manager` is a deep Module. Its Interface is the shared test surface and contains no JSON, IPC, OpenSSH, filesystem, socket, or Swift details.

```go
type Manager interface {
    Execute(context.Context, Command) (Outcome, error)
    Snapshot(context.Context, Scope) (Snapshot, error)
    Watch(context.Context, WatchOptions) (SnapshotStream, error)
    Close(context.Context) error
}

type SnapshotStream interface {
    Next(context.Context) (Snapshot, error)
    Close() error
}
```

`Command` is a sealed set of domain actions. Each command carries a manager-lifetime `CommandID` for idempotent retries and may carry an expected `Revision` when protecting interactive edits. IPC versioning and malformed JSON are Adapter concerns, not Manager errors.

`Snapshot` is an immutable, deterministically ordered view at one manager-wide, monotonically increasing `Revision`. `Scope` selects one Development Host or all hosts. The first `SnapshotStream.Next` returns a complete snapshot at the subscription point; later calls return complete, coalesced snapshots only after caller-visible changes. Slow watchers never block reconciliation and receive `ErrResyncRequired` when they must open a new stream.

`Execute` is concurrency-safe and linearizable. Persistent intent commands return after an atomic configuration write. Forward commands return after the Local Endpoint is allocated or an explainable typed error occurs. Cancellation stops waiting but does not imply rollback after a command has committed.

## Hidden Implementation

The Implementation uses one coordinator actor for global revision, configuration, subscriptions, and Development Host registration, plus one actor per Development Host for its Forwarding Session, Discovery Baseline, Listener Lifetimes, one-time decisions, and reconciliation. Scanner, SSH process, local binding, and proxy I/O run concurrently outside actor mailboxes and report facts back to the owning actor. Proxy data never traverses the control-plane actor.

The Implementation hides system `ssh -G` and `ssh -T -D`, scanner validation, SOCKS readiness, dual-stack local allocation, policy evidence, disappearance grace, reconnect jitter, JSONC persistence, sensitive-data redaction, and idle shutdown.

## Internal Seams

- **OpenSSH Seam — true external:** production process Adapter and scripted test Adapter own alias validation, SSH/SOCKS lifecycle, observation frames, and exit classification.
- **Clock/random Seam — in-process controllability:** real and deterministic Adapters drive reconnect and disappearance tests.
- **Configuration — local-substitutable:** tests use temporary directories and the real JSONC implementation; no filesystem Interface leaks through the external Seam.
- **Loopback networking — local-substitutable:** tests use real loopback sockets except narrowly scripted allocation failures where an internal Adapter earns its keep.
- **IPC — external Adapter:** Unix sockets/named pipes and `creachadair/jrpc2` translate project-owned JSON-RPC methods into Commands, Outcomes, and Snapshots; wire DTOs remain separate from domain types.

Tests exercise behavior through the Manager Interface. Separate Adapter tests verify OpenSSH argv/environment/exit classification and IPC framing; they do not duplicate Manager reconciliation tests.

## Rejected shapes

- Exposing only an actor `Dispatch(Message)` minimized method count but moved the semantic Interface into a large message union and reduced caller discoverability.
- Resource handles for hosts, policies, forwards, and state produced a wide, shallow Interface and encouraged IPC to mirror internal resources.
- Fine-grained domain events would force every client to implement a reducer. Complete coalesced Snapshots provide greater caller Leverage until measured payload size proves otherwise.
