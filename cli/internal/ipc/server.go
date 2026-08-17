// Package ipc hosts the per-user Manager singleton (ADR-0016): a
// long-lived process owns the Manager and answers compatible CLI and
// desktop clients over a current-user-only Unix socket speaking the
// JSON-RPC v1 protocol (docs/design/ipc-protocol.md).
package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"ssh-forward/cli/internal/core"
	managerjsonrpc "ssh-forward/cli/internal/jsonrpc"
)

// ErrAlreadyRunning reports that a live manager already owns the endpoint.
var ErrAlreadyRunning = errors.New("manager is already running")

// probeLive answers whether a live listener currently owns the socket:
// the connection probe is the proof of ownership diagnostics-and-recovery
// requires before a stale socket file may be removed.
func probeLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Serve runs the per-user Manager singleton on the Unix socket at path
// until ctx ends. A listener that answers the probe means the singleton
// already runs (ErrAlreadyRunning); a socket file no one answers is stale
// and is replaced. Each accepted connection gets one JSON-RPC session
// (hello-first, bounded frames, watch notifications).
func Serve(ctx context.Context, path string, manager core.Manager) error {
	if probeLive(path) {
		return ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	defer func() { _ = os.Remove(path) }()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			// The listener closed with the context: a clean stop. A
			// failure mid-service on a local Unix socket is
			// indistinguishable here; either way the serve loop ends.
			return nil
		}
		go func() { _ = managerjsonrpc.Serve(ctx, conn, manager) }()
	}
}
