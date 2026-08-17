package core

import "context"

type Revision uint64

type ForwardID string

type HostAlias string

// MaxHostAliasLength is the maximum length of a HostAlias in bytes. The
// SSH adapter (openssh) enforces it before invoking ssh, so the bound lives
// with the domain type.
const MaxHostAliasLength = 255

type AddressFamily string

const (
	FamilyIPv4 AddressFamily = "ipv4"
	FamilyIPv6 AddressFamily = "ipv6"
)

// ConnectionState is the Forwarding Session's life cycle as the mirror and
// the Snapshot expose it. Transition table — who may write which state (the
// only two legal writers are the Manager mirror under the Manager lock and
// the host actor under its own lock):
//
//	disconnected → connecting: manager.ensureConnected patches the
//	    mirror while the actor is unarmed (armed() guard). The
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
// The Manager declares Connecting through beginConnectionLocked (manager.go).
// SessionDisposition (below) collapses suspend/closed into the same terminal write.
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

type ForwardSnapshot struct {
	ID                 ForwardID
	RemotePort         uint16
	RemoteFamily       AddressFamily
	AllocatedLocalPort uint16
	LocalFamilies      []AddressFamily
}

type HostSnapshot struct {
	Alias                HostAlias
	Connection           ConnectionState
	Discovery            DiscoverySnapshot
	ListenerObservations []ListenerObservation
	Forwards             []ForwardSnapshot
}

type ErrorKind string

const (
	ErrorLocalPortConflict ErrorKind = "local_port_conflict"
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
	Snapshot(context.Context) (Snapshot, error)
	Watch(context.Context) (SnapshotStream, error)
	Close(context.Context) error
}
