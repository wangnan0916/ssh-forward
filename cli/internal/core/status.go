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

type DiscoveryStatus struct {
	State      DiscoveryState `json:"state"`
	Diagnostic string         `json:"diagnostic,omitempty"`
}

type ForwardStatus struct {
	Port       uint16       `json:"port"`
	State      ForwardState `json:"state"`
	Diagnostic string       `json:"diagnostic,omitempty"`
	Automatic  bool         `json:"automatic,omitempty"`
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

// ForwardingIntent is the persistent intent a Manager reconciles. Remembered
// Ports stay forwarded independently of listener state. Working Directory
// Rules create Automatic Forwards only for currently matching listeners.
type ForwardingIntent struct {
	RememberedPorts       []uint16
	WorkingDirectoryRules []string
}

var ErrManagerClosed = errors.New("manager is closed")

// Backend is the true-external OpenSSH seam. Observe blocks while a fixed
// remote scanner is alive and emits complete listener sets. Forward blocks
// while one ssh -L process is alive and calls ready after the local port binds.
type Backend interface {
	Observe(context.Context, HostAlias, func([]Listener)) error
	Forward(context.Context, HostAlias, uint16, func()) error
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
	Close(context.Context) error
}
