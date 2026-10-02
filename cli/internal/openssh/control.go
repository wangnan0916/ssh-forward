package openssh

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
)

type controlForward struct {
	flag string
	spec string
}

// A failed mux client does not necessarily mean the master rejected its
// request. Preserve that distinction until installation cleanup is complete.
type controlCommandError struct {
	diagnostic error
	uncertain  bool
	rejected   bool
}

func (e *controlCommandError) Error() string { return e.diagnostic.Error() }
func (e *controlCommandError) Unwrap() error { return e.diagnostic }

func (a *Adapter) runControl(ctx context.Context, operation string, forward *controlForward) error {
	arguments := append(a.masterClientArguments(), "-O", operation)
	if forward != nil {
		arguments = append(arguments, "-o", "ExitOnForwardFailure=yes", forward.flag, forward.spec)
	}
	arguments = append(arguments, a.target)
	return a.runControlCommand(ctx, operation, arguments)
}

func (a *Adapter) runControlCommand(ctx context.Context, operation string, arguments []string) error {
	ctx, cancel := context.WithTimeout(ctx, a.controlTimeout)
	defer cancel()
	command := a.commandContext(ctx, arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	command.Stdout = io.Discard
	command.Stderr = stderr
	err := command.Run()
	message := strings.ToLower(strings.TrimSpace(stderr.String()))
	if err == nil {
		// OpenSSH may exit zero even when a mux cancellation failed. An
		// explicitly absent forward is safe; other warnings are not proof
		// that installation/cancellation completed.
		if operation == "cancel" && strings.Contains(message, "port not forwarded") {
			return nil
		}
		if message == "" || (operation != "forward" && operation != "cancel") {
			return nil
		}
	}
	uncertain := command.Process != nil
	rejected := false
	if ctx.Err() == nil && command.ProcessState != nil && command.ProcessState.ExitCode() >= 0 {
		// Only a completed client with an explicit refusal establishes that
		// no forward was installed. A healthy master alone proves nothing.
		if strings.Contains(message, "mux_client_forward: forwarding request failed:") ||
			strings.Contains(message, "cannot listen to port") ||
			strings.Contains(message, "address already in use") {
			uncertain = false
			rejected = true
		}
	}
	diagnostic := classifyError(err, message)
	if uncertain {
		diagnostic = backendError("transport_unavailable")
	}
	return &controlCommandError{diagnostic: diagnostic, uncertain: uncertain, rejected: rejected}
}

func (a *Adapter) cancelForward(master *sshMaster, forward controlForward) {
	// A retiring worker must never cancel a forward on a replacement master
	// that has reused this host's control socket. Serialize this check with
	// replacement and skip cleanup once the original transport has exited.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.master != master {
		return
	}
	select {
	case <-master.done:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.waitDelay)
	defer cancel()
	if err := a.runControl(ctx, "cancel", &forward); err != nil {
		_ = a.stopMaster(context.Background(), master)
	}
}

func (a *Adapter) controlPath() string {
	identity := cmp.Or(a.controlIdentity, a.target)
	digest := sha256.Sum256([]byte(identity))
	// Commands run inside the private control directory. A bounded relative
	// path avoids the short Unix-domain socket path limit on macOS while still
	// ignoring any user-configured ControlPath.
	return fmt.Sprintf("master-%x", digest[:12])
}

func (a *Adapter) masterClientArguments() []string {
	return []string{"-F", "/dev/null", "-S", a.controlPath()}
}
