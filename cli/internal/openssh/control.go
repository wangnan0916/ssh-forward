package openssh

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"
)

type controlForward struct {
	flag string
	spec string
}

func (a *Adapter) runControl(ctx context.Context, operation string, forward *controlForward) error {
	arguments := append(a.masterClientArguments(), "-O", operation)
	if forward != nil {
		arguments = append(arguments, "-o", "ExitOnForwardFailure=yes", forward.flag, forward.spec)
	}
	arguments = append(arguments, a.target)
	return a.runControlCommand(ctx, arguments)
}

func (a *Adapter) runLegacyControl(ctx context.Context, operation string) error {
	arguments := append(a.configArguments(), "-S", legacyControlSocketTemplate, "-O", operation, a.target)
	return a.runControlCommand(ctx, arguments)
}

func (a *Adapter) runControlCommand(ctx context.Context, arguments []string) error {
	ctx, cancel := context.WithTimeout(ctx, a.controlTimeout)
	defer cancel()
	command := a.commandContext(ctx, arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	command.Stdout = io.Discard
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return classifyError(err, stderr.String())
	}
	return nil
}

func (a *Adapter) stopLegacyMaster(ctx context.Context) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, a.waitDelay)
	defer cancel()
	if err := a.runLegacyControl(cleanupCtx, "exit"); err != nil {
		return legacyCleanupError(ctx, cleanupCtx)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-cleanupCtx.Done():
			return legacyCleanupError(ctx, cleanupCtx)
		case <-ticker.C:
			if err := a.runLegacyControl(cleanupCtx, "check"); err != nil {
				return legacyCleanupError(ctx, cleanupCtx)
			}
		}
	}
}

func legacyCleanupError(parent, cleanup context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if cleanup.Err() != nil {
		return backendError("master_start_timeout")
	}
	return nil
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
