package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/diagnostics"
)

func diagnoseDiscovery(ctx context.Context, opts Options, host string) DoctorCheck {
	discoveryCtx, cancel := context.WithTimeout(ctx, doctorDiscoveryTimeout)
	defer cancel()

	controlDirectory, err := os.MkdirTemp("", "ssh-forward-doctor-")
	if err != nil {
		return failedDoctorCheck("discovery", err.Error(), "Check that the temporary directory is writable.")
	}
	defer os.RemoveAll(controlDirectory)

	targets, _, err := HostList(opts.ConfigPath)
	if err != nil {
		return failedDoctorCheck("discovery", err.Error(), "Repair the host registry or configuration.")
	}
	target, found := targets[host]
	if !found {
		target = HostTarget{Target: host}
	}
	if target.Diagnostic != "" {
		return failedDoctorCheck("discovery", "Discovered target needs connection settings.", "Use host add NAME --target DESTINATION with explicit options.")
	}
	adapter, err := NewOpenSSHAdapter(opts.SSHConfigPath, controlDirectory, host, target)
	if err != nil {
		return failedDoctorCheck("discovery", err.Error(), "Check the OpenSSH executable and --ssh-config path.")
	}
	defer adapter.Close(discoveryCtx)
	listeners, err := probeDiscovery(discoveryCtx, adapter)
	if err != nil {
		diagnostic := core.ErrorDiagnostic(err)
		detail, fix := diagnostics.DoctorAdvice(diagnostic, host)
		return failedDoctorCheck("discovery", detail, fix)
	}
	return okDoctorCheck("discovery", fmt.Sprintf("remote scan returned %d reachable TCP listener(s)", len(listeners)))
}

type listenerObserver interface {
	Observe(context.Context, func([]core.Listener)) error
}

func probeDiscovery(ctx context.Context, backend listenerObserver) ([]core.Listener, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type probeResult struct {
		listeners []core.Listener
		err       error
	}
	results := make(chan probeResult, 1)
	offerResult := func(result probeResult) {
		select {
		case results <- result:
		default:
		}
	}
	go func() {
		err := backend.Observe(ctx, func(listeners []core.Listener) {
			offerResult(probeResult{listeners: slices.Clone(listeners)})
		})
		if err == nil {
			err = errors.New("remote listener scan ended before returning a snapshot")
		}
		offerResult(probeResult{err: err})
	}()
	select {
	case result := <-results:
		return result.listeners, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
