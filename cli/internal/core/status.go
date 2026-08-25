package core

import (
	"context"
	"errors"
)

type HostAlias string

const MaxHostAliasLength = 255

type DiscoveryState string

const (
	DiscoveryConnecting DiscoveryState = "connecting"
	DiscoveryActive     DiscoveryState = "active"
	DiscoveryFailed     DiscoveryState = "failed"
)

type ForwardState string

const (
	ForwardStarting ForwardState = "starting"
	ForwardActive   ForwardState = "active"
	ForwardFailed   ForwardState = "failed"
)

type ForwardDirection string

const (
	RemoteToLocal ForwardDirection = "remote_to_local"
	LocalToRemote ForwardDirection = "local_to_remote"
)

type DiscoveryStatus struct {
	State      DiscoveryState `json:"state"`
	Diagnostic string         `json:"diagnostic,omitempty"`
}

type ForwardStatus struct {
	Direction           ForwardDirection `json:"direction"`
	RemotePort          uint16           `json:"remote_port"`
	PreferredRemotePort uint16           `json:"preferred_remote_port,omitempty"`
	PreferredLocalPort  uint16           `json:"preferred_local_port,omitempty"`
	LocalPort           uint16           `json:"local_port"`
	State               ForwardState     `json:"state"`
	Diagnostic          string           `json:"diagnostic,omitempty"`
	Automatic           bool             `json:"automatic,omitempty"`
	AllowFallback       bool             `json:"allow_fallback,omitempty"`
}

// Listener is a remote TCP listener reachable through the IPv4 loopback
// address. Process metadata is best-effort because procfs may hide another
// user's process details.
type Listener struct {
	Port             uint16 `json:"port"`
	App              string `json:"app,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type Status struct {
	Host                  HostAlias       `json:"host"`
	Discovery             DiscoveryStatus `json:"discovery"`
	Listeners             []Listener      `json:"listeners"`
	Forwards              []ForwardStatus `json:"forwards"`
	WorkingDirectoryRules []string        `json:"working_directory_rules,omitempty"`
}

type RememberedForward struct {
	RemotePort    uint16 `json:"remote_port"`
	LocalPort     uint16 `json:"local_port"`
	AllowFallback bool   `json:"allow_fallback,omitempty"`
}

// WithDefaults applies the implicit same-port fallback policy used when the
// local port is omitted.
func (forward RememberedForward) WithDefaults() RememberedForward {
	if forward.LocalPort == 0 {
		forward.LocalPort = forward.RemotePort
		forward.AllowFallback = true
	}
	return forward
}

type PublishedForward struct {
	LocalPort  uint16 `json:"local_port"`
	RemotePort uint16 `json:"remote_port"`
}

func (forward PublishedForward) WithDefaults() PublishedForward {
	if forward.RemotePort == 0 {
		forward.RemotePort = forward.LocalPort
	}
	return forward
}

type ForwardTarget struct {
	Direction  ForwardDirection
	RemotePort uint16
	LocalPort  uint16
}

// ForwardingIntent is the persistent intent a Manager reconciles. Remembered
// and Published Forwards stay live independently of listener state. Working
// Directory Rules create Automatic Forwards only for currently matching
// listeners.
type ForwardingIntent struct {
	RememberedForwards    []RememberedForward `json:"remembered_forwards"`
	PublishedForwards     []PublishedForward  `json:"published_forwards"`
	WorkingDirectoryRules []string            `json:"working_directory_rules"`
}

var ErrManagerClosed = errors.New("manager is closed")

// Backend is the true-external OpenSSH seam. Observe blocks while a fixed
// remote scanner is alive and emits complete listener sets. Forward blocks
// while one exact logical forward exists and calls ready after its listening
// endpoint binds. Close releases the shared transport after observation and
// forwards have stopped.
type Backend interface {
	Observe(context.Context, HostAlias, func([]Listener)) error
	Forward(context.Context, HostAlias, ForwardTarget, func()) error
	Close(context.Context) error
}

// BackendError carries a small user-facing diagnostic without exposing raw
// SSH stderr through Status.
type BackendError struct {
	Diagnostic string
}

func (e *BackendError) Error() string { return e.Diagnostic }

func ErrorDiagnostic(err error) string {
	var backend *BackendError
	if errors.As(err, &backend) && backend.Diagnostic != "" {
		return backend.Diagnostic
	}
	return "transport_unavailable"
}

type Manager interface {
	Status(context.Context) (Status, error)
	UpdateIntent(context.Context, ForwardingIntent) error
	Close(context.Context) error
}
