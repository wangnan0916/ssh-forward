package openssh

import (
	"context"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *Adapter) Observe(ctx context.Context, emit func([]core.Listener)) error {
	master, err := a.ensureMaster(ctx)
	if err != nil {
		return err
	}
	arguments := append(a.masterClientArguments(), "-T", "-o", "ControlMaster=no", a.target, scannerBootstrap)
	command := a.command(arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
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
	var scanErr error
	if err := writeScannerScript(stdin); err != nil {
		_ = terminateProcess(command)
	} else {
		scanErr = scanListeners(stdout, stdin, emit)
		if scanErr != nil {
			_ = terminateProcess(command)
		}
	}
	_ = stdin.Close()
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
