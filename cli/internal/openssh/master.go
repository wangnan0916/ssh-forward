package openssh

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

var errAdapterClosed = errors.New("OpenSSH adapter is closed")

const legacyControlSocketTemplate = "master-%C"

type sshMaster struct {
	command *exec.Cmd
	stderr  *boundedBuffer
	done    chan struct{}
	err     error // published by closing done
}

func (a *Adapter) ensureMaster(ctx context.Context) (*sshMaster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errAdapterClosed
	}
	if master := a.master; master != nil {
		select {
		case <-master.done:
			a.master = nil
		default:
			return master, nil
		}
	}
	if err := a.validateAlias(ctx, a.target); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, backendError("invalid_alias")
	}

	// A previous Manager may have died before closing its product-owned master.
	// Stop the pre-alias-hash master first, then ask any current-format stale
	// master to exit before creating the replacement.
	if err := a.stopLegacyMaster(ctx); err != nil {
		return nil, err
	}
	_ = a.runControl(ctx, "exit", nil)
	master, err := a.startMaster()
	if err != nil {
		return nil, err
	}
	if err := a.waitForMaster(ctx, master); err != nil {
		return nil, err
	}
	a.master = master
	return master, nil
}

func (a *Adapter) startMaster() (*sshMaster, error) {
	arguments := append(a.configArguments(),
		"-M", "-N", "-T", "-g", "-S", a.controlPath(),
		"-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=yes", "-o", "ControlPersist=no",
		// TCP can remain apparently established after a reboot or a dropped
		// network path. Encrypted keepalives force the master to exit so the
		// existing discovery/forward retry loops can establish a fresh session.
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1",
		a.target,
	)
	command := a.command(arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	command.Stdout = io.Discard
	command.Stderr = stderr
	configureProcess(command)
	if err := command.Start(); err != nil {
		return nil, err
	}
	master := &sshMaster{command: command, stderr: stderr, done: make(chan struct{})}
	go master.wait()
	return master, nil
}

func (a *Adapter) waitForMaster(ctx context.Context, master *sshMaster) error {
	err := a.awaitReady(ctx, master, "master_start_timeout", func() bool {
		return a.runControl(ctx, "check", nil) == nil
	})
	if err != nil {
		_ = a.stopMaster(ctx, master)
	}
	return err
}

func (m *sshMaster) wait() {
	m.err = m.command.Wait()
	close(m.done)
}

func (m *sshMaster) failure() error {
	return classifyError(m.err, m.stderr.String())
}

func (a *Adapter) stopMaster(ctx context.Context, master *sshMaster) error {
	_ = terminateProcess(master.command)
	stopped := make(chan struct{})
	// Cleanup must continue if the caller's deadline expires.
	go func() {
		defer close(stopped)
		timer := time.NewTimer(a.waitDelay)
		defer timer.Stop()
		select {
		case <-master.done:
		case <-timer.C:
			_ = killProcess(master.command)
			<-master.done
		}
	}()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the private master after Manager workers have
// canceled their individual forward requests and discovery session.
func (a *Adapter) Close(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	master := a.master
	a.master = nil
	a.mu.Unlock()
	if master == nil {
		return nil
	}
	return a.stopMaster(ctx, master)
}
