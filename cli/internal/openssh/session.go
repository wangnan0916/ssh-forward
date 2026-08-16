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

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/proxy"
)

type exitKind string

const (
	exitCancelled      exitKind = "cancelled"
	exitTransient      exitKind = "transient"
	exitAuthentication exitKind = "authentication"
	exitHostKey        exitKind = "host_key"
)

var errInvalidSessionTarget = errors.New("OpenSSH Forwarding Session target must be remote loopback")

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
	for {
		fact, found, scannerClosed := s.facts.pop()
		if found {
			return fact, nil
		}
		if scannerClosed {
			select {
			case <-s.done:
				return nil, s.terminalError()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		select {
		case <-s.facts.notify:
		case <-s.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
	retry := time.NewTicker(10 * time.Millisecond)
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
	connection, err := net.DialTimeout("tcp4", address.String(), 50*time.Millisecond)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		return err
	}
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 5 || response[1] != 0 {
		return errors.New("OpenSSH SOCKS probe rejected no-authentication method")
	}
	return nil
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
		return &core.SessionError{Disposition: core.SessionRetry, Reason: core.SessionReasonTransport}
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
