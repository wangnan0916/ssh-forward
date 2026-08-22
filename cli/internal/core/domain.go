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
	RemoteListeners CapabilityAvailability `json:"remote_listeners"`
	ProcessMetadata CapabilityAvailability `json:"process_metadata"`
}

type DiscoverySnapshot struct {
	State               DiscoveryState      `json:"state"`
	Capability          DiscoveryCapability `json:"capability"`
	BaselineEstablished bool                `json:"baseline_established"`
	Diagnostic          string              `json:"diagnostic"`
}

type ListenerBindScope string

const (
	BindLoopback ListenerBindScope = "loopback"
	BindWildcard ListenerBindScope = "wildcard"
)

type ProcessMetadata struct {
	PID              int      `json:"pid"`
	Executable       string   `json:"executable"`
	WorkingDirectory string   `json:"working_directory"`
	Arguments        []string `json:"arguments"`
}

type ProcessChain struct {
	Processes []ProcessMetadata `json:"processes"`
}

type ListenerObservation struct {
	Family     AddressFamily     `json:"family"`
	BindScope  ListenerBindScope `json:"bind_scope"`
	RemotePort uint16            `json:"remote_port"`
	Processes  []ProcessChain    `json:"process_chains"`
}

type ForwardSnapshot struct {
	ID                 ForwardID       `json:"id"`
	RemotePort         uint16          `json:"remote_port"`
	RemoteFamily       AddressFamily   `json:"remote_family"`
	AllocatedLocalPort uint16          `json:"allocated_local_port"`
	LocalFamilies      []AddressFamily `json:"local_families"`
}

// LocalPortConflict is a Remote Listener whose Local Endpoint could not be
// allocated under the configured conflict policy.
type LocalPortConflict struct {
	RemotePort   uint16            `json:"remote_port"`
	RemoteFamily AddressFamily     `json:"remote_family"`
	BindScope    ListenerBindScope `json:"bind_scope"`
}

// HostSnapshot is the composed host view on a Snapshot.
type HostSnapshot struct {
	Alias                HostAlias             `json:"alias"`
	Connection           ConnectionState       `json:"connection"`
	ConnectionDiagnostic string                `json:"connection_diagnostic,omitempty"`
	Discovery            DiscoverySnapshot     `json:"discovery"`
	ListenerObservations []ListenerObservation `json:"listener_observations"`
	Forwards             []ForwardSnapshot     `json:"forwards"`
	LocalPortConflicts   []LocalPortConflict   `json:"local_port_conflicts,omitempty"`
	PolicyDiagnostic     string                `json:"policy_diagnostic,omitempty"`
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
	Revision Revision      `json:"revision"`
	Host     *HostSnapshot `json:"host,omitempty"`
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
