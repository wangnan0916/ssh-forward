package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
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
	checks := []DoctorCheck{diagnoseConfig(opts.ConfigPath), diagnoseSSHConfig(opts.SSHConfigPath)}
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
		return failedDoctorCheck("openssh", "OpenSSH client was not found on PATH.", "Install OpenSSH and ensure ssh is on PATH.")
	}
	return okDoctorCheck("openssh", path)
}

func diagnoseHost(opts Options) (string, DoctorCheck) {
	host := opts.HostFlag
	if !core.ValidHostName(host) {
		return "", failedDoctorCheck("host", "A host is required for SSH diagnostics.", "Pass --host TARGET.")
	}
	return host, okDoctorCheck("host", host)
}

func diagnoseConfig(path string) DoctorCheck {
	config, err := LoadConfig(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return warningDoctorCheck("config", "no config.jsonc yet", "Remember a host or add a forward to create it.")
	case err != nil:
		return failedDoctorCheck("config", err.Error(), "Repair or remove the invalid config.jsonc file: "+path)
	default:
		intentCount := 0
		for _, rules := range config.model().Rules {
			intentCount += len(rules.Forwards) + len(rules.Published) + len(rules.Directories)
		}
		return okDoctorCheck("config", fmt.Sprintf("%s (%d remembered intent item(s))", path, intentCount))
	}
}

func diagnoseSSHConfig(flag string) DoctorCheck {
	path := SSHConfigPath(flag)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) && flag == "":
		return warningDoctorCheck("ssh-config", "default SSH config does not exist: "+path, "Create ~/.ssh/config or pass --ssh-config PATH.")
	case err != nil:
		return failedDoctorCheck("ssh-config", err.Error(), "Pass a readable OpenSSH config with --ssh-config PATH.")
	case info.IsDir():
		return failedDoctorCheck("ssh-config", path+" is a directory", "Pass an OpenSSH config file with --ssh-config PATH.")
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
			managerCheck = failedDoctorCheck("manager", "the Manager socket is live but did not return status", "Run ssh-forward uninstall, then ssh-forward status.")
		default:
			managerCheck = warningDoctorCheck("manager", "background Manager is not running", "Run ssh-forward status to install or start it.")
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
	checks := []DoctorCheck{okDoctorCheck("manager", fmt.Sprintf("running for %s with discovery %s", status.Host, status.Discovery.State))}

	checks = append(checks, diagnoseForwards(status))
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

func okDoctorCheck(name, detail string) DoctorCheck {
	return DoctorCheck{Name: name, State: DoctorOK, Detail: detail}
}

func warningDoctorCheck(name, detail, fix string) DoctorCheck {
	return DoctorCheck{Name: name, State: DoctorWarning, Detail: detail, Fix: fix}
}

func failedDoctorCheck(name, detail, fix string) DoctorCheck {
	return DoctorCheck{Name: name, State: DoctorFailed, Detail: detail, Fix: fix}
}
