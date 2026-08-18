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

`Snapshot` is an immutable, deterministically ordered view at one manager-wide, monotonically increasing `Revision`; a newly constructed Manager with no host begins at revision `0`. It carries one `Host` (the Manager's single Development Host; nil while none is configured) with Listener Observations, Forwards, Local Port Conflicts, Connection Diagnostic, and Policy Diagnostic. Policy match evidence (which policy fired, and why) is not on the Snapshot. The first `SnapshotStream.Next` returns the complete immutable Snapshot captured at the subscription point; later calls return complete latest-value Snapshots only after caller-visible changes, so revisions may jump. A slow watcher never blocks publication and does not require resync merely because revisions were coalesced. The Manager admits at most 128 streams, permits one in-flight `Next` per stream, treats cancellation as cancellation of only that wait, and makes stream `Close` idempotent.

A Manager constructed with a host and connector starts the Forwarding Session immediately: the host actor publishes Connecting before its connect loop runs, so discovery and Auto-forward proceed without a separate command. A permanent SSH failure leaves the host Disconnected with a Connection Diagnostic until the Manager is restarted.

## Hidden Implementation

The current Implementation builds one canonical immutable Snapshot under the Manager lock for every caller-visible transition and publishes it to bounded latest-value subscriptions. A hidden Forward table gives each Forward sole ownership of its Local Endpoint lifecycle and a stable identity token (`family:scope:port`); the ID string remains `managed:` plus that token. A per-host actor (one per Manager, since one Manager serves one user and one host) is the only writer of Connection, Discovery, and Listener Observations. It owns the Forwarding Session, the data-path Dialer, Discovery ingestion, observation continuity, and reconnect scheduling behind its own lock, and publishes that host view to the Manager. One compose point overlays the Forward table, Local Port Conflicts, and Policy Diagnostic (the latter two live on the reconciler). Arming publishes Connecting under the actor lock (then the Manager lock) before the connect loop runs; the loop publishes from Connected onward. The connect loop runs at most once per arming; a terminal session failure (for example an SSH authentication or host-key rejection) ends the loop. Per-host publication is deduplicated at one point in the actor: no-change states never advance the Manager revision. The Forward table mutates only under the Manager lock; Allocate and Close run outside it.

Policy reconciliation has two wakes. One internal `decide` step turns observations, policies, and hysteresis into create/remove intent; `allocateIntent` runs Allocate outside the Manager lock; `applyIntent` commits policy, the Forward table, and Local Port Conflicts under the lock, then Close after Unlock. Non-conflict allocation failures retry on the next observation without publishing. The observation path keeps two-generation hysteresis: a Managed Forward is created after two consecutive Auto-forward matches and removed after two consecutive observations that no longer match. A saved policy edit (including a change to Ignore) applies against the current observations immediately and resets hysteresis; production Managers poll the policy source every 250ms so CLI `add`/`remove` take effect without waiting for the next scan. A no-change policy poll is a no-op so the ticker cannot fill hysteresis. A Policy Diagnostic-only poll publishes without touching hysteresis. Local Port Conflict from allocation is recorded on the Snapshot until a later allocation succeeds or the listener is no longer desired.

The Implementation hides system `ssh -G` and `ssh -T -D`, scanner framing, SOCKS readiness, dual-stack local allocation (`proxy.NewAllocator` consumes the session Dialer), JSONC stripping and atomic intent writes, policy evaluation, and reconnect jitter. Discovery Diagnostic is produced by one table: adapter DiscoveryChange reasons, observation-sequence gaps, and capability partiality. Connection Diagnostic is produced by one table from a terminal SessionReason. A corrupt policies file keeps last-valid policies and sets Policy Diagnostic on the Snapshot. Core admits ObservationSet and DiscoveryChange facts through one gate; the HostSession parser is a cheap stream-local filter. Scanner frame vocabulary stops at the HostSession adapter. The adapter declares a per-scan observation budget in-band; core retains evidence within `MaxRetained*` and rejects a declaration that would exceed them — parser caps need not equal retention caps. Sensitive-data redaction in logs and idle shutdown are specified but not yet implemented.

## Internal Seams

- **OpenSSH Seam — true external:** `Connect` is the production Adapter's public method and returns a Forwarding Session whose narrow Interface is validated `Next`, constrained `Dial` (`HostSession` embeds `Dialer`), and bounded idempotent `Close`. Alias validation, session start, and the fact buffer behind `Next` stay unexported. Production and scripted Adapters own alias validation, SSH/SOCKS lifecycle, scanner framing, and typed terminal disposition; core admits the resulting ObservationSet.
- **Clock/random Seam — in-process controllability:** real and deterministic Adapters drive reconnect tests.
- **Configuration — local-substitutable:** tests use temporary directories and the real JSONC implementation (`app` strip + atomic write); no filesystem Interface leaks through the external Seam.
- **Loopback networking — local-substitutable:** tests use real loopback sockets except narrowly scripted allocation failures where an internal Adapter earns its keep. Production allocation is `proxy.NewAllocator`.
- **IPC — external Adapter:** `app.Connect` / `app.Serve` own the per-user singleton lifecycle. `jsonrpc` is one module: Unix socket Listen/Dial/Serve plus the JSON-RPC session. Hello is negotiated outside jrpc2; afterwards Dial and Serve share the jrpc2 session. It embeds Snapshots through `cli/internal/snapshot`. CLI `--json` uses that same package and does not import the RPC adapter. Wire DTOs remain separate from domain types. Client and server share one framing module (`Send` / `Recv` / `Close`, with a response-written hook after handshake). A named-pipe adapter is not split out until Windows Local Machine is in scope.

Tests exercise behavior through the Manager Interface. Separate Adapter tests verify OpenSSH argv/environment/exit classification and IPC framing; they do not duplicate Manager reconciliation tests.

## Rejected shapes

- Exposing only an actor `Dispatch(Message)` minimized method count but moved the semantic Interface into a large message union and reduced caller discoverability.
- Resource handles for hosts, policies, forwards, and state produced a wide, shallow Interface and encouraged IPC to mirror internal resources.
- Fine-grained domain events would force every client to implement a reducer. Complete coalesced Snapshots provide greater caller Leverage until measured payload size proves otherwise.
