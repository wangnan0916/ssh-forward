package openssh

import (
	"context"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"ssh-forward/cli/internal/proxy"
)

type ExitKind string

const (
	ExitClean          ExitKind = "clean"
	ExitCancelled      ExitKind = "cancelled"
	ExitTransient      ExitKind = "transient"
	ExitAuthentication ExitKind = "authentication"
	ExitHostKey        ExitKind = "host_key"
)

type ConnectionError struct {
	Kind ExitKind
}

func (e *ConnectionError) Error() string {
	return "OpenSSH connection failed: " + string(e.Kind)
}

type Session struct {
	command *exec.Cmd
	dialer  proxy.Dialer
	done    chan struct{}
	stderr  *boundedBuffer
	closing atomic.Bool

	exitMu   sync.Mutex
	exitKind ExitKind

	closeOnce sync.Once
}

func (s *Session) DialContext(ctx context.Context, target netip.AddrPort) (proxy.HalfCloseConn, error) {
	return s.dialer.DialContext(ctx, target)
}

func (s *Session) Done() <-chan struct{} {
	return s.done
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

func (s *Session) wait() {
	err := s.command.Wait()
	s.exitMu.Lock()
	s.exitKind = classifyExit(err, s.stderr.String(), s.closing.Load())
	s.exitMu.Unlock()
	close(s.done)
}

func (s *Session) ExitKind() ExitKind {
	<-s.done
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitKind
}

func classifyExit(err error, stderr string, cancelled bool) ExitKind {
	if cancelled {
		return ExitCancelled
	}
	if err == nil {
		return ExitClean
	}
	message := strings.ToLower(stderr)
	if strings.Contains(message, "permission denied") || strings.Contains(message, "too many authentication failures") ||
		strings.Contains(message, "no supported authentication methods") {
		return ExitAuthentication
	}
	if strings.Contains(message, "host key verification failed") || strings.Contains(message, "remote host identification has changed") {
		return ExitHostKey
	}
	return ExitTransient
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
