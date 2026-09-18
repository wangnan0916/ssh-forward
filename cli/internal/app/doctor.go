package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
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
		checks = append(checks, diagnoseDiscovery(ctx, opts, host))
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
	host := opts.HostFlag
	if !validTargetName(host) {
		return "", failedDoctorCheck(
			"host", "A host is required for SSH diagnostics.",
			"Pass --host TARGET.",
		)
	}
	return host, okDoctorCheck("host", host)
}

func diagnoseConfig(path string) DoctorCheck {
	config, err := LoadConfig(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return warningDoctorCheck(
			"config", "no config.jsonc yet", "Remember a host or add a forward to create it.",
		)
	case err != nil:
		return failedDoctorCheck(
			"config", err.Error(), "Repair or remove the invalid config.jsonc file: "+path,
		)
	default:
		intentCount := 0
		for _, rules := range config.model().Rules {
			intentCount += len(rules.Forwards) + len(rules.Published) + len(rules.Directories)
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

	statuses, err := manager.AllStatuses(ctx)
	index := slices.IndexFunc(statuses, func(s core.Status) bool { return string(s.Host) == selectedHost })
	if err != nil || index < 0 {
		return unavailableManagerChecks(warningDoctorCheck(
			"manager", "the Manager is running but status is unavailable for "+selectedHost,
			"Run ssh-forward status --host "+selectedHost+" to start or refresh this host.",
		))
	}
	status := statuses[index]
	checks := []DoctorCheck{okDoctorCheck(
		"manager", fmt.Sprintf("running for %s with discovery %s", status.Host, status.Discovery.State),
	)}

	checks = append(checks, diagnoseForwards(status))
	return checks
}

func diagnoseForwards(status core.Status) DoctorCheck {
	failedLocalPorts := make([]int, 0)
	failedRemotePorts := make([]int, 0)
	fixes := make([]string, 0)
	needsPerPortStatus := false
	for _, forward := range status.Forwards {
		if forward.State != core.ForwardFailed {
			continue
		}
		if forward.Direction == core.LocalToRemote {
			failedRemotePorts = append(failedRemotePorts, int(forward.RemotePort))
			_, fix := diagnostics.DoctorAdvice(forward.Diagnostic, string(status.Host))
			if !slices.Contains(fixes, fix) {
				fixes = append(fixes, fix)
			}
		} else {
			failedLocalPorts = append(failedLocalPorts, int(forward.LocalPort))
			fix := diagnostics.DoctorFix(forward.Diagnostic, string(status.Host))
			if fix == "" {
				needsPerPortStatus = true
			} else if !slices.Contains(fixes, fix) {
				fixes = append(fixes, fix)
			}
		}
	}
	if len(failedLocalPorts) == 0 && len(failedRemotePorts) == 0 {
		return okDoctorCheck(
			"forwards", fmt.Sprintf("%d forward(s), none failed", len(status.Forwards)),
		)
	}
	if needsPerPortStatus {
		fixes = append([]string{"Run ssh-forward status for the per-port issue."}, fixes...)
	}
	return failedDoctorCheck(
		"forwards", failedForwardDetail(failedLocalPorts, failedRemotePorts),
		strings.Join(fixes, " "),
	)
}

func failedForwardDetail(localPorts, remotePorts []int) string {
	slices.Sort(localPorts)
	slices.Sort(remotePorts)
	parts := make([]string, 0, 2)
	if len(localPorts) != 0 {
		parts = append(parts, "local port(s): "+joinedPorts(localPorts))
	}
	if len(remotePorts) != 0 {
		parts = append(parts, "Development Host port(s): "+joinedPorts(remotePorts))
	}
	return "failed " + strings.Join(parts, "; ")
}

func joinedPorts(ports []int) string {
	values := make([]string, len(ports))
	for index, port := range ports {
		values[index] = strconv.Itoa(port)
	}
	return strings.Join(values, ", ")
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

func diagnoseDiscovery(ctx context.Context, opts Options, host string) DoctorCheck {
	discoveryCtx, cancel := context.WithTimeout(ctx, doctorDiscoveryTimeout)
	defer cancel()

	controlDirectory, err := os.MkdirTemp("", "ssh-forward-doctor-")
	if err != nil {
		return failedDoctorCheck(
			"discovery", err.Error(), "Check that the temporary directory is writable.",
		)
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
	adapter, err := NewOpenSSHAdapter(opts.SSHConfigPath, controlDirectory)
	if err != nil {
		return failedDoctorCheck(
			"discovery", err.Error(), "Check the OpenSSH executable and --ssh-config path.",
		)
	}
	defer adapter.Close(discoveryCtx)
	adapter.SetConnectionArguments(append([]string{"-o", "BatchMode=yes"}, target.Arguments...))
	listeners, err := probeDiscovery(discoveryCtx, adapter, core.HostAlias(target.Target))
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
