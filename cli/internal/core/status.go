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
	ForwardWaiting  ForwardState = "waiting"
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
}

type Status struct {
	Host             HostAlias       `json:"host"`
	Discovery        DiscoveryStatus `json:"discovery"`
	Listeners        []uint16        `json:"listeners"`
	Forwards         []ForwardStatus `json:"forwards"`
	ConfigDiagnostic string          `json:"config_diagnostic,omitempty"`
}

var ErrManagerClosed = errors.New("manager is closed")

// Backend is the true-external OpenSSH seam. Observe blocks while a fixed
// remote scanner is alive and emits complete port sets. Forward blocks while
// one ssh -L process is alive and calls ready after the local port is bound.
type Backend interface {
	Observe(context.Context, HostAlias, func([]uint16)) error
	Forward(context.Context, HostAlias, uint16, func()) error
}

// PortSource reads the remembered ports for the Manager's host. On error the
// Manager retains the last valid set and reports a configuration diagnostic.
type PortSource func() ([]uint16, error)

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
