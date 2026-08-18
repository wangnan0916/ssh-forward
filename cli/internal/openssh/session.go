package openssh

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

type exitKind string

const (
	exitCancelled      exitKind = "cancelled"
	exitTransient      exitKind = "transient"
	exitAuthentication exitKind = "authentication"
	exitHostKey        exitKind = "host_key"
)

var errInvalidSessionTarget = errors.New("OpenSSH Forwarding Session target must be remote loopback")

// probeRetryInterval and probeTimeout are the SOCKS-readiness probe cadence:
// the probe retries every probeRetryInterval, and each attempt (dial and
// handshake) is budgeted probeTimeout, so a slow start fails fast and the
// waitUntilReady caller's overall timeout owns the total.
const (
	probeRetryInterval = 10 * time.Millisecond
	probeTimeout       = 50 * time.Millisecond
)

type Session struct {
	command     *exec.Cmd
	dialer      proxy.Dialer
	done        chan struct{}
	stderr      *boundedBuffer
	facts       *sessionFactQueue
	scannerDone chan struct{}
	closing     atomic.Bool

	exitMu   sync.Mutex
	exitKind exitKind

	closeOnce sync.Once
}

func (s *Session) DialContext(ctx context.Context, target netip.AddrPort) (proxy.HalfCloseConn, error) {
	ipv4Loopback := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if target.Port() == 0 || (target.Addr() != ipv4Loopback && target.Addr() != netip.IPv6Loopback()) {
		return nil, errInvalidSessionTarget
	}
	return s.dialer.DialContext(ctx, target)
}

func (s *Session) Next(ctx context.Context) (core.SessionFact, error) {
	fact, drained, err := s.facts.next(ctx, s.done)
	if err != nil {
		return nil, err
	}
	if drained {
		return nil, s.terminalError()
	}
	return fact, nil
}

func (s *Session) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		_ = terminateProcess(s.command)
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		_ = killProcess(s.command)
		return ctx.Err()
	}
}

func (s *Session) readScanner(stdout io.Reader) {
	defer close(s.scannerDone)
	defer s.facts.close()
	scanObservationFrames(stdout, s.facts.push)
}

func (s *Session) wait() {
	err := s.command.Wait()
	_ = terminateProcess(s.command)
	_ = killProcess(s.command)
	<-s.scannerDone
	s.exitMu.Lock()
	s.exitKind = classifyExit(err, s.stderr.String(), s.closing.Load())
	s.exitMu.Unlock()
	close(s.done)
}

func (s *Session) exitedKind() exitKind {
	<-s.done
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitKind
}

func (s *Session) terminalError() error {
	return sessionErrorForExit(s.exitedKind())
}

func (s *Session) waitUntilReady(ctx context.Context, address netip.AddrPort, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	retry := time.NewTicker(probeRetryInterval)
	defer retry.Stop()
	for {
		if probeSOCKS(address) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return sessionErrorForExit(s.exitedKind())
		case <-deadline.C:
			return errors.New("OpenSSH SOCKS readiness timed out")
		case <-retry.C:
		}
	}
}

func probeSOCKS(address netip.AddrPort) error {
	connection, err := net.DialTimeout("tcp4", address.String(), probeTimeout)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(probeTimeout)); err != nil {
		return err
	}
	return proxy.NegotiateMethod(connection)
}

// retryTransportError is the single construction of the retry/transport
// SessionError. The exit translation's default branch and the connect-time
// start-failure fallback share it, so a Reason or Disposition change lands
// in one place.
func retryTransportError() *core.SessionError {
	return &core.SessionError{Disposition: core.SessionRetry, Reason: core.SessionReasonTransport}
}

// sessionErrorForExit is the single translation from an OpenSSH exit class
// to the SessionError core consumes; both session-end and connect-time paths
// use it, so the exit taxonomy never leaks out of this package.
func sessionErrorForExit(kind exitKind) *core.SessionError {
	switch kind {
	case exitCancelled:
		return &core.SessionError{Disposition: core.SessionClosed, Reason: core.SessionReasonClosed}
	case exitAuthentication:
		return &core.SessionError{Disposition: core.SessionSuspend, Reason: core.SessionReasonAuthentication}
	case exitHostKey:
		return &core.SessionError{Disposition: core.SessionSuspend, Reason: core.SessionReasonHostKey}
	default:
		return retryTransportError()
	}
}

func classifyExit(err error, stderr string, cancelled bool) exitKind {
	if cancelled {
		return exitCancelled
	}
	if err != nil {
		message := strings.ToLower(stderr)
		if strings.Contains(message, "permission denied") || strings.Contains(message, "too many authentication failures") ||
			strings.Contains(message, "no supported authentication methods") {
			return exitAuthentication
		}
		if strings.Contains(message, "host key verification failed") || strings.Contains(message, "remote host identification has changed") {
			return exitHostKey
		}
	}
	return exitTransient
}

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return len(data), nil
	}
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return len(data), nil
	}
	overflow := max(len(b.data)+len(data)-b.limit, 0)
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

const maxQueuedSessionFacts = 8

// sessionFactQueue is Session.Next's private buffer. On overflow it replaces
// the newest pending fact rather than dropping the oldest, so the head —
// where the first ObservationSet sits — survives. That first set is the
// Discovery Baseline and is never evicted. Every later set may be replaced
// by newer evidence. A real scanner emits at most two facts per connection,
// so with a cap of 8 the overflow branches are unreachable today; they keep
// the Baseline guarantee if a future producer outpaces the consumer.
type sessionFactQueue struct {
	mu                sync.Mutex
	items             []core.SessionFact
	notify            chan struct{}
	closed            bool
	firstSetDelivered bool
	terminalDiscovery *core.DiscoveryChange
}

func newSessionFactQueue() *sessionFactQueue {
	return &sessionFactQueue{
		items:  make([]core.SessionFact, 0, maxQueuedSessionFacts),
		notify: make(chan struct{}, 1),
	}
}

func (q *sessionFactQueue) push(fact core.SessionFact) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if change, ok := fact.(core.DiscoveryChange); ok && change.State == core.DiscoveryFailed {
		q.terminalDiscovery = &change
		q.signalLocked()
		return
	}
	if len(q.items) < maxQueuedSessionFacts {
		q.items = append(q.items, fact)
		q.signalLocked()
		return
	}
	last := len(q.items) - 1
	if !q.firstSetDelivered {
		if _, protected := q.items[last].(core.ObservationSet); protected {
			q.signalLocked()
			return
		}
	}
	q.items[last] = fact
	q.signalLocked()
}

func (q *sessionFactQueue) next(ctx context.Context, sessionDone <-chan struct{}) (core.SessionFact, bool, error) {
	for {
		q.mu.Lock()
		fact, found, drained := q.popLocked()
		q.mu.Unlock()
		if found {
			return fact, false, nil
		}
		if drained {
			select {
			case <-sessionDone:
				return nil, true, nil
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
		select {
		case <-q.notify:
		case <-sessionDone:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

func (q *sessionFactQueue) popLocked() (core.SessionFact, bool, bool) {
	if len(q.items) == 0 {
		if q.terminalDiscovery == nil {
			return nil, false, q.closed
		}
		fact := *q.terminalDiscovery
		q.terminalDiscovery = nil
		return fact, true, q.closed
	}
	fact := q.items[0]
	copy(q.items, q.items[1:])
	q.items = q.items[:len(q.items)-1]
	if _, ok := fact.(core.ObservationSet); ok {
		q.firstSetDelivered = true
	}
	return fact, true, q.closed
}

func (q *sessionFactQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.signalLocked()
}

func (q *sessionFactQueue) signalLocked() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
