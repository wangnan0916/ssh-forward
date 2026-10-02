package openssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	forwardProbeInterval = 25 * time.Millisecond
	forwardDialTimeout   = 50 * time.Millisecond
)

func (a *Adapter) Forward(ctx context.Context, target core.ForwardTarget, ready func()) error {
	master, err := a.ensureMaster(ctx)
	if err != nil {
		return err
	}
	forward, err := controlForwardFor(target)
	if err != nil {
		return err
	}
	if target.Direction == core.RemoteToLocal &&
		a.localPortAvailable != nil && !a.localPortAvailable(target.LocalPort) {
		return backendError("local_port_conflict")
	}
	// Installation and cleanup must refer to the same transport generation.
	// A reconnect may otherwise reuse the socket after ensureMaster returns.
	a.mu.Lock()
	if a.master != master {
		a.mu.Unlock()
		return backendError("transport_unavailable")
	}
	select {
	case <-master.done:
		a.mu.Unlock()
		return master.failure()
	default:
	}
	err = a.startForward(ctx, target.Direction, forward)
	if failure, ok := errors.AsType[*controlCommandError](err); ok && failure.uncertain {
		// The request may have committed before its acknowledgement was lost.
		// Retire this generation while still holding the installation lock:
		// canceling an unowned tuple could affect an existing forward, and a
		// canceled caller must not prevent cleanup. Other workers reconnect.
		_ = a.stopMaster(context.Background(), master)
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	defer a.cancelForward(master, forward)
	switch target.Direction {
	case core.RemoteToLocal:
		if err := a.waitForLocalForward(ctx, master, target.LocalPort); err != nil {
			return err
		}
	case core.LocalToRemote:
		if err := a.verifyRemoteLoopbackForward(ctx, master, target.RemotePort); err != nil {
			return err
		}
	}
	ready()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-master.done:
		return master.failure()
	}
}

func localLoopbackPortAvailable(port uint16) bool {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func (a *Adapter) startForward(ctx context.Context, direction core.ForwardDirection, forward controlForward) error {
	err := a.runControl(ctx, "forward", &forward)
	if err == nil {
		return nil
	}
	if failure, ok := errors.AsType[*controlCommandError](err); !ok || !failure.rejected {
		return err
	}
	// Only an explicit rejection can be attributed to a binding conflict.
	// A command that never started and an uncertain result must not trigger
	// fallback, even if a subsequent master health check would succeed.
	if checkErr := a.runControl(ctx, "check", nil); checkErr == nil {
		if direction == core.LocalToRemote {
			err = backendError("remote_port_unavailable")
		} else {
			err = backendError("local_port_conflict")
		}
	}
	return err
}

func controlForwardFor(target core.ForwardTarget) (controlForward, error) {
	switch target.Direction {
	case core.RemoteToLocal:
		return controlForward{flag: "-L", spec: fmt.Sprintf("0.0.0.0:%d:127.0.0.1:%d", target.LocalPort, target.RemotePort)}, nil
	case core.LocalToRemote:
		return controlForward{flag: "-R", spec: fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", target.RemotePort, target.LocalPort)}, nil
	default:
		return controlForward{}, backendError("invalid_forward_direction")
	}
}

func (a *Adapter) waitForLocalForward(ctx context.Context, master *sshMaster, port uint16) error {
	return a.awaitReady(ctx, master, "forward_start_timeout", func() bool {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), forwardDialTimeout)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
}

// Both a master and an imported listener become ready by polling, while
// cancellation, transport exit, and the startup deadline remain authoritative.
func (a *Adapter) awaitReady(ctx context.Context, master *sshMaster, diagnostic string, probe func() bool) error {
	deadline := time.NewTimer(a.readyTimeout)
	ticker := time.NewTicker(forwardProbeInterval)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-master.done:
			return master.failure()
		case <-deadline.C:
			return backendError(diagnostic)
		case <-ticker.C:
			if probe() {
				return nil
			}
		}
	}
}
