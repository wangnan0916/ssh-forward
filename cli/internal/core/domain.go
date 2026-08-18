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

// ConnectionState is the Forwarding Session's life cycle as the Snapshot
// exposes it. The host actor is the only writer; the Manager mirror follows
// via the actor's publication callback.
//
//	disconnected → connecting: the actor arms (startIfNeeded) and publishes
//	    Connecting before the connect loop runs.
//	connecting → connected: the actor's connect loop after the session
//	    passes readiness.
//	connected → connecting: the actor after a retryable session end.
//	* → disconnected: the actor's terminal paths. The actor always writes
//	    active=false in the same critical section as the Disconnected
//	    publication.
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
// full. Evidence producers attach it to an ObservationSet; discoveryDiagnostic
// is the single table that turns it (and gaps, and DiscoveryChange reasons)
// into the wire Diagnostic. Empty means there is no partiality to explain.
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

// LocalPortConflict is a Remote Listener whose Local Endpoint could not be
// allocated under the configured conflict policy.
type LocalPortConflict struct {
	RemotePort   uint16
	RemoteFamily AddressFamily
	BindScope    ListenerBindScope
}

type HostSnapshot struct {
	Alias                HostAlias
	Connection           ConnectionState
	Discovery            DiscoverySnapshot
	ListenerObservations []ListenerObservation
	Forwards             []ForwardSnapshot
	LocalPortConflicts   []LocalPortConflict
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
