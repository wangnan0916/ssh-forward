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
// the internal process-assembly package; they are not part of Manager's API.
type HostSession interface {
	proxy.Dialer
	Done() <-chan struct{}
	Close(context.Context) error
}

type HostConnector interface {
	Connect(context.Context, HostAlias) (HostSession, error)
}

type hostSession = HostSession
type hostConnector = HostConnector

func (m *manager) connect() {
	defer m.workers.Done()
	retryAttempt := 0
	for {
		session, err := m.connector.Connect(m.ctx, m.host)
		if err != nil {
			if !retryableConnectionError(err) {
				m.mu.Lock()
				if !m.closed && m.connection != ConnectionDisconnected {
					m.connection = ConnectionDisconnected
					m.publishLocked()
				}
				m.mu.Unlock()
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
		m.publishLocked()
		m.mu.Unlock()

		select {
		case <-m.ctx.Done():
			closeHostSession(session)
			return
		case <-session.Done():
		}
		retry := retryableSessionExit(session)
		closeHostSession(session)
		m.mu.Lock()
		m.session = nil
		m.dialer.Set(nil)
		if !m.closed {
			if retry {
				m.connection = ConnectionConnecting
			} else {
				m.connection = ConnectionDisconnected
			}
			m.publishLocked()
		}
		m.mu.Unlock()
		if !retry || !m.waitToReconnect(retryAttempt) {
			return
		}
		retryAttempt++
	}
}

func closeHostSession(session hostSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = session.Close(ctx)
}

type permanentConnectionError struct {
	err error
}

func (e permanentConnectionError) Error() string {
	return e.err.Error()
}

func (permanentConnectionError) Retryable() bool {
	return false
}

func retryableConnectionError(err error) bool {
	var classified interface{ Retryable() bool }
	return !errors.As(err, &classified) || classified.Retryable()
}

func retryableSessionExit(session hostSession) bool {
	classified, ok := session.(interface{ RetryableExit() bool })
	return !ok || classified.RetryableExit()
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
