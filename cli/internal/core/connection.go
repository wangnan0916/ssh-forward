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
	Sequence     uint64
	Capability   DiscoveryCapability
	Observations []ListenerObservation
}

func (ObservationSet) isSessionFact() {}

// DiscoveryReason is the domain vocabulary an adapter uses to report why
// Discovery is degraded or failed. Framing, streams, and scanner scripts
// stay inside the HostSession adapter; these reasons name what the
// Development Host observation could not provide. discoveryDiagnostic is
// the single translation to the wire Diagnostic.
type DiscoveryReason string

const (
	// ReasonObservationInvalid reports observation input the adapter could
	// not turn into a valid ObservationSet.
	ReasonObservationInvalid DiscoveryReason = "observation_invalid"
	// ReasonObservationLost reports that the adapter could not continue
	// producing observations.
	ReasonObservationLost DiscoveryReason = "observation_lost"
	// ReasonSessionInvalid reports a fact core admission rejected
	// (defense against a misbehaving adapter).
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
}

func (e *SessionError) Error() string {
	return "Development Host session ended: " + string(e.Reason)
}

// connectionDiagnostic is the single translation from a terminal SessionReason
// to the Snapshot field. Retryable transport and a user-closed session have
// no diagnostic: Needs Attention is for failures the user must fix.
func connectionDiagnostic(reason SessionReason) string {
	switch reason {
	case SessionReasonInvalidAlias:
		return "invalid_alias"
	case SessionReasonAuthentication:
		return "authentication_failed"
	case SessionReasonHostKey:
		return "host_key_failed"
	default:
		return ""
	}
}
