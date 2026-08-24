package openssh

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const controlSocketTemplate = "master-%C"

var errAdapterClosed = errors.New("OpenSSH adapter is closed")

type sshMaster struct {
	command *exec.Cmd
	stderr  *boundedBuffer
	done    chan struct{}
	err     error // published by closing done
}

func (a *Adapter) ensureMaster(ctx context.Context, host core.HostAlias) (*sshMaster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errAdapterClosed
	}
	if master := a.masters[host]; master != nil {
		select {
		case <-master.done:
			delete(a.masters, host)
		default:
			return master, nil
		}
	}

	// A previous Manager may have died before closing its product-owned master.
	// Ask that stale master to exit before creating the replacement.
	_ = a.runControl(ctx, host, "exit", "")
	master, err := a.startMaster(host)
	if err != nil {
		return nil, err
	}
	if err := a.waitForMaster(ctx, host, master); err != nil {
		return nil, err
	}
	a.masters[host] = master
	return master, nil
}

func (a *Adapter) startMaster(host core.HostAlias) (*sshMaster, error) {
	arguments := append(a.configArguments(),
		"-M", "-N", "-T", "-S", controlSocketTemplate,
		"-o", "ControlMaster=yes", "-o", "ControlPersist=no",
		string(host),
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

func (a *Adapter) waitForMaster(ctx context.Context, host core.HostAlias, master *sshMaster) error {
	deadline := time.NewTimer(a.readyTimeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.stopMaster(master)
			return ctx.Err()
		case <-master.done:
			return master.failure()
		case <-deadline.C:
			a.stopMaster(master)
			return backendError("master_start_timeout")
		case <-ticker.C:
			if err := a.runControl(ctx, host, "check", ""); err == nil {
				return nil
			}
		}
	}
}

func (a *Adapter) runControl(ctx context.Context, host core.HostAlias, operation, forward string) error {
	arguments := append(a.configArguments(), "-S", controlSocketTemplate, "-O", operation)
	if forward != "" {
		arguments = append(arguments, "-o", "ExitOnForwardFailure=yes", "-L", forward)
	}
	arguments = append(arguments, string(host))
	command := a.commandContext(ctx, arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	command.Stdout = io.Discard
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return classifyError(err, stderr.String())
	}
	return nil
}

func (a *Adapter) cancelForward(host core.HostAlias, forward string) {
	ctx, cancel := context.WithTimeout(context.Background(), a.waitDelay)
	defer cancel()
	_ = a.runControl(ctx, host, "cancel", forward)
}

func (m *sshMaster) wait() {
	m.err = m.command.Wait()
	close(m.done)
}

func (m *sshMaster) failure() error {
	return classifyError(m.err, m.stderr.String())
}

func (a *Adapter) stopMaster(master *sshMaster) {
	_ = terminateProcess(master.command)
	timer := time.NewTimer(a.waitDelay)
	defer timer.Stop()
	select {
	case <-master.done:
	case <-timer.C:
		_ = killProcess(master.command)
		<-master.done
	}
}

// Close stops every product-owned OpenSSH master after Manager workers have
// canceled their individual forward requests and discovery session.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	masters := make([]*sshMaster, 0, len(a.masters))
	for _, master := range a.masters {
		masters = append(masters, master)
	}
	a.masters = nil
	a.mu.Unlock()
	for _, master := range masters {
		a.stopMaster(master)
	}
	return nil
}
