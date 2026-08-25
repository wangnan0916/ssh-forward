package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/diagnostics"
)

type DoctorState string

const (
	DoctorOK      DoctorState = "ok"
	DoctorWarning DoctorState = "warning"
	DoctorFailed  DoctorState = "failed"
)

const doctorDiscoveryTimeout = 15 * time.Second

type DoctorCheck struct {
	Name   string      `json:"name"`
	State  DoctorState `json:"state"`
	Detail string      `json:"detail"`
	Fix    string      `json:"fix,omitempty"`
}

type DoctorReport struct {
	Healthy bool          `json:"healthy"`
	Host    string        `json:"host,omitempty"`
	Checks  []DoctorCheck `json:"checks"`
}

// Diagnose inspects local configuration, the resident Manager, and one real
// remote listener scan without installing, restarting, or changing anything.
func Diagnose(ctx context.Context, opts Options) DoctorReport {
	opts = opts.WithDefaults()
	checks := []DoctorCheck{
		diagnoseConfig(opts.ConfigPath),
		diagnoseSSHConfig(opts.SSHConfigPath),
	}
	opensshCheck := diagnoseOpenSSH()
	host, hostCheck := diagnoseHost(opts)
	checks = append(checks, opensshCheck, hostCheck)
	checks = append(checks, diagnoseManager(ctx, opts, host)...)

	if opensshCheck.State == DoctorOK && hostCheck.State == DoctorOK {
		checks = append(checks, diagnoseDiscovery(ctx, opts.SSHConfigPath, host))
	}
	healthy := !slices.ContainsFunc(checks, func(check DoctorCheck) bool {
		return check.State == DoctorFailed
	})
	return DoctorReport{Healthy: healthy, Host: host, Checks: checks}
}

func diagnoseOpenSSH() DoctorCheck {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return failedDoctorCheck(
			"openssh", "OpenSSH client was not found on PATH.",
			"Install OpenSSH and ensure ssh is on PATH.",
		)
	}
	return okDoctorCheck("openssh", path)
}

func diagnoseHost(opts Options) (string, DoctorCheck) {
	opts.Interactive = false
	opts.PickHost = nil
	host, err := ResolveHost(opts)
	if err != nil {
		return "", failedDoctorCheck(
			"host", err.Error(),
			"Pass --host ALIAS or pin one with: ssh-forward default ALIAS",
		)
	}
	return host, okDoctorCheck("host", host)
}

func diagnoseConfig(path string) DoctorCheck {
	config, err := LoadConfig(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return warningDoctorCheck(
			"config", "no config.jsonc yet", "Pin a host or add a forward to create it.",
		)
	case err != nil:
		return failedDoctorCheck(
			"config", err.Error(), "Repair or remove the invalid config.jsonc file: "+path,
		)
	default:
		intentCount := 0
		for _, forwards := range config.RememberedForwards {
			intentCount += len(forwards)
		}
		for _, rules := range config.WorkingDirectoryRules {
			intentCount += len(rules)
		}
		return okDoctorCheck(
			"config", fmt.Sprintf("%s (%d remembered intent item(s))", path, intentCount),
		)
	}
}

func diagnoseSSHConfig(flag string) DoctorCheck {
	path := SSHConfigPath(flag)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) && flag == "":
		return warningDoctorCheck(
			"ssh-config", "default SSH config does not exist: "+path,
			"Create ~/.ssh/config or pass --ssh-config PATH.",
		)
	case err != nil:
		return failedDoctorCheck(
			"ssh-config", err.Error(), "Pass a readable OpenSSH config with --ssh-config PATH.",
		)
	case info.IsDir():
		return failedDoctorCheck(
			"ssh-config", path+" is a directory", "Pass an OpenSSH config file with --ssh-config PATH.",
		)
	default:
		return okDoctorCheck("ssh-config", path)
	}
}

func diagnoseManager(ctx context.Context, opts Options, selectedHost string) []DoctorCheck {
	manager, err := dialManager(ctx, opts.Layout.Socket, opts.Version)
	if err != nil {
		var managerCheck DoctorCheck
		switch {
		case errors.Is(err, ErrIncompatibleManager):
			managerCheck = failedDoctorCheck(
				"manager", "the running Manager uses an incompatible binary or protocol",
				"Run ssh-forward status to replace it with this binary.",
			)
		case socketLive(opts.Layout.Socket):
			managerCheck = failedDoctorCheck(
				"manager", "the Manager socket is live but did not return status",
				"Run ssh-forward uninstall, then ssh-forward status.",
			)
		default:
			managerCheck = warningDoctorCheck(
				"manager", "background Manager is not running",
				"Run ssh-forward status to install or start it.",
			)
		}
		return unavailableManagerChecks(managerCheck)
	}
	defer manager.Close(context.Background())

	status, err := manager.Status(ctx)
	if err != nil {
		return unavailableManagerChecks(failedDoctorCheck(
			"manager", err.Error(), "Run ssh-forward uninstall, then ssh-forward status.",
		))
	}
	checks := []DoctorCheck{okDoctorCheck(
		"manager", fmt.Sprintf("running for %s with discovery %s", status.Host, status.Discovery.State),
	)}
	if selectedHost != "" && status.Host != core.HostAlias(selectedHost) {
		checks[0] = warningDoctorCheck(
			"manager", fmt.Sprintf("running for %s while %s is selected", status.Host, selectedHost),
			"Run ssh-forward status to switch the Manager.",
		)
	}

	failedPorts := make([]int, 0)
	for _, forward := range status.Forwards {
		if forward.State == core.ForwardFailed {
			failedPorts = append(failedPorts, int(forward.LocalPort))
		}
	}
	if len(failedPorts) == 0 {
		checks = append(checks, okDoctorCheck(
			"forwards", fmt.Sprintf("%d forward(s), none failed", len(status.Forwards)),
		))
	} else {
		slices.Sort(failedPorts)
		ports := make([]string, len(failedPorts))
		for index, port := range failedPorts {
			ports[index] = fmt.Sprint(port)
		}
		checks = append(checks, failedDoctorCheck(
			"forwards", "failed local port(s): "+strings.Join(ports, ", "),
			"Run ssh-forward status for the per-port issue.",
		))
	}
	return checks
}

func unavailableManagerChecks(managerCheck DoctorCheck) []DoctorCheck {
	return []DoctorCheck{
		managerCheck,
		warningDoctorCheck(
			"forwards", "forward health is unavailable while the Manager is stopped",
			"Run ssh-forward status to start the Manager.",
		),
	}
}

func diagnoseDiscovery(ctx context.Context, sshConfig, host string) DoctorCheck {
	discoveryCtx, cancel := context.WithTimeout(ctx, doctorDiscoveryTimeout)
	defer cancel()

	controlDirectory, err := os.MkdirTemp("", "ssh-forward-doctor-")
	if err != nil {
		return failedDoctorCheck(
			"discovery", err.Error(), "Check that the temporary directory is writable.",
		)
	}
	defer os.RemoveAll(controlDirectory)

	adapter, err := NewOpenSSHAdapter(sshConfig, controlDirectory)
	if err != nil {
		return failedDoctorCheck(
			"discovery", err.Error(), "Check the OpenSSH executable and --ssh-config path.",
		)
	}
	defer adapter.Close(discoveryCtx)
	listeners, err := probeDiscovery(discoveryCtx, adapter, core.HostAlias(host))
	if err != nil {
		diagnostic := core.ErrorDiagnostic(err)
		detail, fix := diagnostics.DoctorAdvice(diagnostic, host)
		return failedDoctorCheck(
			"discovery", detail, fix,
		)
	}
	return okDoctorCheck(
		"discovery", fmt.Sprintf("remote scan returned %d reachable TCP listener(s)", len(listeners)),
	)
}

type listenerObserver interface {
	Observe(context.Context, core.HostAlias, func([]core.Listener)) error
}

func probeDiscovery(
	ctx context.Context,
	backend listenerObserver,
	host core.HostAlias,
) ([]core.Listener, error) {
	type probeResult struct {
		listeners   []core.Listener
		err         error
		hasSnapshot bool
	}
	results := make(chan probeResult, 1)
	offerResult := func(result probeResult) {
		select {
		case results <- result:
		default:
		}
	}
	go func() {
		err := backend.Observe(ctx, host, func(listeners []core.Listener) {
			offerResult(probeResult{listeners: slices.Clone(listeners), hasSnapshot: true})
		})
		offerResult(probeResult{err: err})
	}()
	select {
	case result := <-results:
		if result.hasSnapshot {
			return result.listeners, nil
		}
		if result.err == nil {
			return nil, errors.New("remote listener scan ended before returning a snapshot")
		}
		return nil, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func okDoctorCheck(name, detail string) DoctorCheck {
	return DoctorCheck{Name: name, State: DoctorOK, Detail: detail}
}

func warningDoctorCheck(name, detail, fix string) DoctorCheck {
	return DoctorCheck{Name: name, State: DoctorWarning, Detail: detail, Fix: fix}
}

func failedDoctorCheck(name, detail, fix string) DoctorCheck {
	return DoctorCheck{Name: name, State: DoctorFailed, Detail: detail, Fix: fix}
}
