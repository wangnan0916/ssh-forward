package openssh

import (
	"context"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *Adapter) Observe(ctx context.Context, host core.HostAlias, emit func([]core.Listener)) error {
	alias := string(host)
	if !validAlias(alias) {
		return backendError("invalid_alias")
	}
	master, err := a.ensureMaster(ctx, host)
	if err != nil {
		return err
	}
	arguments := append(a.masterClientArguments(host),
		"-T", "-o", "ControlMaster=no",
		alias, "sh", "-s",
	)
	command := a.command(arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stdin = strings.NewReader(scannerScript)
	command.Stderr = stderr
	configureProcess(command)
	if err := command.Start(); err != nil {
		return err
	}
	stopContextWatch := context.AfterFunc(ctx, func() { _ = terminateProcess(command) })
	defer stopContextWatch()
	stopMasterWatch := make(chan struct{})
	defer close(stopMasterWatch)
	go func() {
		select {
		case <-master.done:
			_ = terminateProcess(command)
		case <-stopMasterWatch:
		}
	}()
	scanErr := scanListenerFrames(stdout, emit)
	if scanErr != nil {
		_ = terminateProcess(command)
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-master.done:
		return master.failure()
	default:
	}
	if scanErr != nil {
		return backendError("discovery_invalid")
	}
	return classifyError(waitErr, stderr.String())
}
