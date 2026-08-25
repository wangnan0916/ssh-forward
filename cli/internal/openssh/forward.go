package openssh

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	forwardProbeInterval = 25 * time.Millisecond
	forwardDialTimeout   = 50 * time.Millisecond
)

func (a *Adapter) Forward(
	ctx context.Context,
	host core.HostAlias,
	target core.ForwardTarget,
	ready func(),
) error {
	alias := string(host)
	if !validAlias(alias) {
		return backendError("invalid_alias")
	}
	master, err := a.ensureMaster(ctx, host)
	if err != nil {
		return err
	}
	forward, err := controlForwardFor(target)
	if err != nil {
		return err
	}
	if err := a.startForward(ctx, host, target.Direction, forward); err != nil {
		return err
	}
	defer a.cancelForward(host, master, forward)
	switch target.Direction {
	case core.RemoteToLocal:
		if err := a.waitForLocalForward(ctx, master, target.LocalPort); err != nil {
			return err
		}
	case core.LocalToRemote:
		if err := a.verifyRemoteLoopbackForward(ctx, host, master, target.RemotePort); err != nil {
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

func (a *Adapter) startForward(
	ctx context.Context,
	host core.HostAlias,
	direction core.ForwardDirection,
	forward controlForward,
) error {
	err := a.runControl(ctx, host, "forward", &forward)
	if err == nil {
		return nil
	}
	// The mux client can report only a generic failure when OpenSSH cannot bind
	// the requested endpoint. If the master is still healthy, classify the
	// failure according to the endpoint owned by this direction.
	if checkErr := a.runControl(ctx, host, "check", nil); checkErr == nil {
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
		return controlForward{
			flag: "-L",
			spec: fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", target.LocalPort, target.RemotePort),
		}, nil
	case core.LocalToRemote:
		return controlForward{
			flag: "-R",
			spec: fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", target.RemotePort, target.LocalPort),
		}, nil
	default:
		return controlForward{}, backendError("invalid_forward_direction")
	}
}

func (a *Adapter) waitForLocalForward(ctx context.Context, master *sshMaster, port uint16) error {
	deadline := time.NewTimer(a.readyTimeout)
	probe := time.NewTicker(forwardProbeInterval)
	defer deadline.Stop()
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-master.done:
			return master.failure()
		case <-deadline.C:
			return backendError("forward_start_timeout")
		case <-probe.C:
			connection, err := net.DialTimeout(
				"tcp4", fmt.Sprintf("127.0.0.1:%d", port), forwardDialTimeout,
			)
			if err != nil {
				continue
			}
			_ = connection.Close()
			return nil
		}
	}
}
