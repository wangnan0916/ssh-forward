package core

import (
	"context"
	"net"
	"net/netip"
)

// HalfCloseConn is a connection that can close its write side independently
// of the read side. The Forwarding Session data path and Local Endpoints
// share this shape so a client FIN still drains the remote response.
type HalfCloseConn interface {
	net.Conn
	CloseWrite() error
}

// Dialer is the Forwarding Session data path: one Remote Listener address
// in, one half-closeable connection out. HostSession embeds it; Local
// Endpoint allocation consumes it. Production (OpenSSH SOCKS) and tests
// (direct TCP, scripted sessions) are the two adapters that justify the seam.
type Dialer interface {
	DialContext(context.Context, netip.AddrPort) (HalfCloseConn, error)
}

// HostSession and HostConnector form the true-external transport seam
// composed by the app package; they are not part of Manager's interface.
type HostSession interface {
	Dialer
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
	Sequence         uint64
	ScannerVersion   int
	ScannerChecksum  string
	Capability       DiscoveryCapability
	CapabilityReason CapabilityReason
	Budget           ObservationBudget
	Observations     []ListenerObservation
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

// DiscoveryReason is the vocabulary scanner-side facts may use to report
// why Discovery is degraded or failed. The actor owns the single translation
// from reason to wire diagnostic, so the user-visible Diagnostic strings
// have exactly one producer.
type DiscoveryReason string

const (
	// ReasonFrameInvalid reports a scanner frame that failed to parse.
	ReasonFrameInvalid DiscoveryReason = "frame_invalid"
	// ReasonStreamFailed reports the scanner output stream ending in error.
	ReasonStreamFailed DiscoveryReason = "stream_failed"
	// ReasonSessionInvalid reports a fact the actor's re-validation gate
	// rejected (defense against a misbehaving adapter).
	ReasonSessionInvalid DiscoveryReason = "session_invalid"
)

type DiscoveryChange struct {
	State      DiscoveryState
	Capability DiscoveryCapability
	Reason     DiscoveryReason
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
