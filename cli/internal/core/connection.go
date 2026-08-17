package core

import (
	"context"

	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

// HostSession and HostConnector form the true-external transport seam composed
// by the app package's assembly point (app.NewManager); they are not part of
// Manager's interface.
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

type hostSession = HostSession
type hostConnector = HostConnector
