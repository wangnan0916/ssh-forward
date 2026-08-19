package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// ErrAlreadyRunning reports that a live manager already owns the endpoint.
var ErrAlreadyRunning = errors.New("manager is already running")

func probeUnix(path string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Live reports whether a manager currently answers at path.
func Live(path string) bool {
	return probeUnix(path, 250*time.Millisecond)
}

// Wait blocks until a live manager answers at path or the deadline passes.
func Wait(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if probeUnix(path, 100*time.Millisecond) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("manager socket did not become ready within %s", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Endpoint is a claimed Unix socket. Close releases the listener and the
// socket file.
type Endpoint struct {
	path     string
	listener net.Listener

	closeOnce sync.Once
	closeErr  error
}

// Listen claims the per-user socket. A live listener is ErrAlreadyRunning;
// a socket file no one answers is stale and is replaced.
func Listen(path string) (*Endpoint, error) {
	if Live(path) {
		return nil, ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &Endpoint{path: path, listener: listener}, nil
}

// Close stops the listener and removes the socket file. It is idempotent.
func (e *Endpoint) Close() error {
	e.closeOnce.Do(func() {
		e.closeErr = e.listener.Close()
		_ = os.Remove(e.path)
	})
	return e.closeErr
}

// Serve accepts JSON-RPC sessions until ctx ends or Close runs.
func (e *Endpoint) Serve(ctx context.Context, manager core.Manager) error {
	go func() {
		<-ctx.Done()
		_ = e.Close()
	}()
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			return nil
		}
		go func() { _ = ServeConn(ctx, conn, manager) }()
	}
}

// Dial connects to the running singleton and negotiates the JSON-RPC v1
// session, returning a Manager whose operations are remote calls.
func Dial(ctx context.Context, path string) (core.Manager, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return DialConn(ctx, conn)
}

// Serve runs the per-user Manager singleton on the Unix socket at path
// until ctx ends. Each accepted connection gets one JSON-RPC session.
func Serve(ctx context.Context, path string, manager core.Manager) error {
	endpoint, err := Listen(path)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	return endpoint.Serve(ctx, manager)
}
