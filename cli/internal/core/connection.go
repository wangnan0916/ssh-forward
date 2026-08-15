package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

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
	Resync          bool
	ScannerVersion  int
	ScannerChecksum string
	Capability      DiscoveryCapability
	Observations    []ListenerObservation
}

func (ObservationSet) isSessionFact() {}

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

func (m *manager) connect() {
	defer m.workers.Done()
	retryAttempt := 0
	for {
		session, err := m.connector.Connect(m.ctx, m.host)
		if err != nil {
			if sessionDisposition(err) != SessionRetry {
				m.publishConnectionFailure()
				return
			}
			if !m.waitToReconnect(retryAttempt) {
				return
			}
			retryAttempt++
			continue
		}

		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			closeHostSession(session)
			return
		}
		retryAttempt = 0
		m.session = session
		m.dialer.Set(session)
		m.connection = ConnectionConnected
		m.discovery = startingDiscovery()
		m.lastObservationSequence = 0
		m.publishLocked()
		m.mu.Unlock()

		err = m.consumeSession(session)
		closeHostSession(session)
		disposition := sessionDisposition(err)

		m.mu.Lock()
		m.session = nil
		m.dialer.Set(nil)
		if !m.closed {
			if disposition == SessionRetry {
				m.connection = ConnectionConnecting
			} else {
				m.connection = ConnectionDisconnected
			}
			m.discovery = stoppedDiscovery()
			m.publishLocked()
		}
		m.mu.Unlock()
		if disposition != SessionRetry || !m.waitToReconnect(retryAttempt) {
			return
		}
		retryAttempt++
	}
}

func (m *manager) consumeSession(session hostSession) error {
	for {
		fact, err := session.Next(m.ctx)
		if err != nil {
			return err
		}
		m.applySessionFact(fact)
	}
}

func (m *manager) publishConnectionFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.connection == ConnectionDisconnected {
		return
	}
	m.connection = ConnectionDisconnected
	m.discovery = stoppedDiscovery()
	m.publishLocked()
}

func closeHostSession(session hostSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = session.Close(ctx)
}

func sessionDisposition(err error) SessionDisposition {
	if errors.Is(err, context.Canceled) {
		return SessionClosed
	}
	var sessionError *SessionError
	if errors.As(err, &sessionError) {
		return sessionError.Disposition
	}
	return SessionRetry
}

func (m *manager) waitToReconnect(attempt int) bool {
	return m.retryWait(m.ctx, m.retryDelay(attempt))
}

func exponentialJitterDelay(attempt int) time.Duration {
	const (
		initial = 250 * time.Millisecond
		maximum = 30 * time.Second
	)
	delay := initial
	for range min(attempt, 7) {
		delay = min(delay*2, maximum)
	}
	jitter := delay / 5
	return delay - jitter + time.Duration(rand.Int64N(int64(2*jitter)+1))
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

var errTransportUnavailable = errors.New("Development Host transport is unavailable")

type currentDialer struct {
	mu     sync.RWMutex
	dialer proxy.Dialer
}

func (d *currentDialer) Set(dialer proxy.Dialer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialer = dialer
}

func (d *currentDialer) DialContext(ctx context.Context, target netip.AddrPort) (proxy.HalfCloseConn, error) {
	d.mu.RLock()
	dialer := d.dialer
	d.mu.RUnlock()
	if dialer == nil {
		return nil, errTransportUnavailable
	}
	return dialer.DialContext(ctx, target)
}
