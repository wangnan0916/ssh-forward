package core

import (
	"context"

	"ssh-forward/cli/internal/proxy"
)

// HostSession and HostConnector form the true-external transport seam used by
// the internal process-assembly package; they are not part of Manager's interface.
type HostSession interface {
	proxy.Dialer
	Next(context.Context) (SessionFact, error)
	Close(context.Context) error
}

type HostConnector interface {
	Connect(context.Context, HostAlias) (HostSession, error)
}

type SessionFact interface {
	isSessionFact()
}

type ObservationSet struct {
	Sequence        uint64
	ScannerVersion  int
	ScannerChecksum string
	Capability      DiscoveryCapability
	Budget          ObservationBudget
	Observations    []ListenerObservation
}

func (ObservationSet) isSessionFact() {}

// ObservationBudget is the evidence the adapter declares its scans are
// bounded to; core validates it against its own retention caps so a
// mismatched scanner cannot silently exceed what a full scan may retain.
type ObservationBudget struct {
	Listeners      int
	Sockets        int
	ProcessRecords int
	MetadataBytes  int
}

type DiscoveryChange struct {
	State      DiscoveryState
	Capability DiscoveryCapability
	Diagnostic string
}

func (DiscoveryChange) isSessionFact() {}

type SessionDisposition string

const (
	SessionRetry   SessionDisposition = "retry"
	SessionSuspend SessionDisposition = "suspend"
	SessionClosed  SessionDisposition = "closed"
)

type SessionReason string

const (
	SessionReasonInvalidAlias   SessionReason = "invalid_alias"
	SessionReasonAuthentication SessionReason = "authentication"
	SessionReasonHostKey        SessionReason = "host_key"
	SessionReasonTransport      SessionReason = "transport"
	SessionReasonProtocol       SessionReason = "protocol"
	SessionReasonClosed         SessionReason = "closed"
)

type SessionError struct {
	Disposition SessionDisposition
	Reason      SessionReason
	Diagnostic  string
}

func (e *SessionError) Error() string {
	return "Development Host session ended: " + string(e.Reason)
}

type hostSession = HostSession
type hostConnector = HostConnector
