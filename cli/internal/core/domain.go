package core

import "context"

type Revision uint64

type CommandID string

type ForwardID string

type HostAlias string

// MaxHostAliasLength is the maximum length of a HostAlias in bytes. Both
// adapters enforce it — the wire adapter (jsonrpc) on command parameters and
// the SSH adapter (openssh) before invoking ssh — so the bound lives with
// the domain type and the adapters reference it.
const MaxHostAliasLength = 255

type AddressFamily string

const (
	FamilyAuto AddressFamily = "auto"
	FamilyIPv4 AddressFamily = "ipv4"
	FamilyIPv6 AddressFamily = "ipv6"
)

// ValidAddressFamily is the single family whitelist. The IPC Adapter
// pre-checks with it so an invalid family fails as wire-invalid parameters,
// and manualTarget re-enforces it as the authoritative defense; adding a
// family means editing this one switch. Like MaxHostAliasLength above, the
// whitelist lives with the domain type it constrains.
func ValidAddressFamily(family AddressFamily) bool {
	switch family {
	case FamilyAuto, FamilyIPv4, FamilyIPv6:
		return true
	default:
		return false
	}
}

// ConnectionState is the Forwarding Session's life cycle as the mirror and
// the Snapshot expose it. Transition table — who may write which state (the
// only two legal writers are the Manager mirror under the Manager lock and
// the host actor under its own lock):
//
//	disconnected → connecting: manager.beginConnectionLocked patches the
//	    mirror on a command while the actor is unarmed (armed() guard). The
//	    actor's startIfNeeded then re-states Connecting in its own state and
//	    arms; both writes share the armed() projection as the single guard.
//	connecting → connected: the actor's connect loop after the session
//	    passes readiness (a.state.Connection = ConnectionConnected).
//	connected → connecting: the actor after a retryable session end
//	    (sessionDisposition == SessionRetry); the mirror follows via the
//	    publication callback.
//	* → disconnected: the actor's terminal paths — non-retryable session
//	    end and publishConnectionFailure. The actor always writes
//	    active=false in the same critical section, so the mirror's
//	    disconnected state is equivalent to the actor being unarmed
//	    (round-6 C1 invariant).
//
// Commands never write Connection directly: they declare through
// beginConnectionLocked, which the three constraints of lock order,
// same-revision outcome, and no-wait arming keep as the one Manager-side
// write (manager.go). SessionDisposition (below) collapses suspend/closed
// into the same terminal write.
type ConnectionState string

const (
	ConnectionDisconnected ConnectionState = "disconnected"
	ConnectionConnecting   ConnectionState = "connecting"
	ConnectionConnected    ConnectionState = "connected"
)

type DiscoveryState string

const (
	DiscoveryStopped  DiscoveryState = "stopped"
	DiscoveryStarting DiscoveryState = "starting"
	DiscoveryHealthy  DiscoveryState = "healthy"
	DiscoveryDegraded DiscoveryState = "degraded"
	DiscoveryFailed   DiscoveryState = "failed"
)

type CapabilityAvailability string

const (
	CapabilityUnavailable CapabilityAvailability = "unavailable"
	CapabilityPartial     CapabilityAvailability = "partial"
	CapabilityFull        CapabilityAvailability = "full"
)

type DiscoveryCapability struct {
	RemoteListeners CapabilityAvailability
	SocketIdentity  CapabilityAvailability
	ProcessMetadata CapabilityAvailability
}

// CapabilityReason explains why a Discovery Capability dimension is not
// full. The evidence producers attach it to an ObservationSet (the scanner
// parser: scanner-declared or recomputed from frames; core: retention-cap
// truncation), and the actor translates it to the user-visible wire
// Diagnostic through its single table. Empty means there is no partiality
// to explain (the capability is full, or a failure path owns the
// Diagnostic).
type CapabilityReason string

const (
	CapabilityReasonNone              CapabilityReason = ""
	CapabilityReasonScannerReported   CapabilityReason = "scanner_reported"
	CapabilityReasonEvidenceMissing   CapabilityReason = "evidence_missing"
	CapabilityReasonEvidenceTruncated CapabilityReason = "evidence_truncated"
)

type DiscoverySnapshot struct {
	State               DiscoveryState
	Capability          DiscoveryCapability
	BaselineEstablished bool
	ScannerVersion      int
	ScannerChecksum     string
	Diagnostic          string
}

type ListenerBindScope string

const (
	BindLoopback ListenerBindScope = "loopback"
	BindWildcard ListenerBindScope = "wildcard"
)

type SocketIdentity string

type ProcessMetadata struct {
	PID              int
	Executable       string
	WorkingDirectory string
	Arguments        []string
}

type ProcessChain struct {
	Processes []ProcessMetadata
}

type ListenerObservation struct {
	Family           AddressFamily
	BindScope        ListenerBindScope
	RemotePort       uint16
	SocketIdentities []SocketIdentity
	Processes        []ProcessChain
}

type ForwardKind string

const (
	ForwardManual  ForwardKind = "manual"
	ForwardManaged ForwardKind = "managed"
)

type Command interface {
	isCommand()
}

type AddManualForward struct {
	CommandID  CommandID
	Host       HostAlias
	RemotePort uint16
	Family     AddressFamily
}

func (AddManualForward) isCommand() {}

type RemoveForward struct {
	CommandID CommandID
	ForwardID ForwardID
}

func (RemoveForward) isCommand() {}

// ApproveListener creates a Managed Forward for the current Listener
// Lifetime through One-time Approval: the approval lasts exactly one
// Listener Lifetime and retires when the verdict turns ended or replaced.
// Family is optional (FamilyAuto or empty matches the first Listener on
// the port).
type ApproveListener struct {
	CommandID  CommandID
	Host       HostAlias
	RemotePort uint16
	Family     AddressFamily
}

func (ApproveListener) isCommand() {}

// SuppressListener asks no further questions during the current Listener
// Lifetime; the suppression retires with the lifetime. Family is optional
// (FamilyAuto or empty matches the first Listener on the port).
type SuppressListener struct {
	CommandID  CommandID
	Host       HostAlias
	RemotePort uint16
	Family     AddressFamily
}

func (SuppressListener) isCommand() {}

type OutcomeKind string

const (
	OutcomeForwardAdded        OutcomeKind = "forward_added"
	OutcomeForwardRemoved      OutcomeKind = "forward_removed"
	OutcomeApprovalRecorded    OutcomeKind = "approval_recorded"
	OutcomeSuppressionRecorded OutcomeKind = "suppression_recorded"
)

type ErrorKind string

const (
	ErrorInvalidCommand    ErrorKind = "invalid_command"
	ErrorUnknownHost       ErrorKind = "unknown_host"
	ErrorCommandIDConflict ErrorKind = "command_id_conflict"
	ErrorLocalPortConflict ErrorKind = "local_port_conflict"
	ErrorForwardNotFound   ErrorKind = "forward_not_found"
	ErrorListenerNotFound  ErrorKind = "listener_not_found"
	ErrorManagerClosed     ErrorKind = "manager_closed"
	ErrorWatchLimit        ErrorKind = "watch_limit"
)

type DomainError struct {
	Kind      ErrorKind
	Retryable bool
}

func (e *DomainError) Error() string {
	return string(e.Kind)
}

type Outcome struct {
	Kind     OutcomeKind
	Revision Revision
	Forward  ForwardSnapshot
}

type ForwardSnapshot struct {
	ID                 ForwardID
	Kind               ForwardKind
	RemotePort         uint16
	RemoteFamily       AddressFamily
	AllocatedLocalPort uint16
	LocalFamilies      []AddressFamily
}

// ListenerAskSnapshot is one Remote Listener currently needing a user
// decision: first observed after the Discovery Baseline, no policy or
// One-time Suppression governs it, and no policy matched automatically.
type ListenerAskSnapshot struct {
	Family     AddressFamily
	BindScope  ListenerBindScope
	RemotePort uint16
}

type HostSnapshot struct {
	Alias                HostAlias
	Connection           ConnectionState
	Discovery            DiscoverySnapshot
	ListenerObservations []ListenerObservation
	ListenerLifetimes    []ListenerLifetimeSnapshot
	AskListeners         []ListenerAskSnapshot
	Forwards             []ForwardSnapshot
}

// Snapshot is the complete Manager state for its one Development Host.
// Host is nil while no Development Host is configured.
type Snapshot struct {
	Revision Revision
	Host     *HostSnapshot
}
type SnapshotStream interface {
	Next(context.Context) (Snapshot, error)
	Close() error
}

type Manager interface {
	Execute(context.Context, Command) (Outcome, error)
	Snapshot(context.Context) (Snapshot, error)
	Watch(context.Context) (SnapshotStream, error)
	Close(context.Context) error
}
