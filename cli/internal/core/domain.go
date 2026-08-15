package core

import "context"

type Revision uint64

type CommandID string

type ForwardID string

type HostAlias string

type AddressFamily string

const (
	FamilyAuto AddressFamily = "auto"
	FamilyIPv4 AddressFamily = "ipv4"
	FamilyIPv6 AddressFamily = "ipv6"
)

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

const ForwardManual ForwardKind = "manual"

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

type OutcomeKind string

const (
	OutcomeForwardAdded   OutcomeKind = "forward_added"
	OutcomeForwardRemoved OutcomeKind = "forward_removed"
)

type ErrorKind string

const (
	ErrorInvalidCommand    ErrorKind = "invalid_command"
	ErrorUnknownHost       ErrorKind = "unknown_host"
	ErrorCommandIDConflict ErrorKind = "command_id_conflict"
	ErrorLocalPortConflict ErrorKind = "local_port_conflict"
	ErrorForwardNotFound   ErrorKind = "forward_not_found"
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

type HostSnapshot struct {
	Alias                HostAlias
	Connection           ConnectionState
	Discovery            DiscoverySnapshot
	ListenerObservations []ListenerObservation
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
