package app

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/diagnostics"
)

func diagnoseForwards(status core.Status) DoctorCheck {
	failedLocalPorts := make([]int, 0)
	failedRemotePorts := make([]int, 0)
	fixes := make([]string, 0)
	needsPerPortStatus := false
	for _, forward := range status.Forwards {
		if forward.State != core.ForwardFailed {
			continue
		}
		fix := diagnostics.DoctorFix(forward.Diagnostic, string(status.Host))
		if forward.Direction == core.LocalToRemote {
			failedRemotePorts = append(failedRemotePorts, int(forward.RemotePort))
			_, fix = diagnostics.DoctorAdvice(forward.Diagnostic, string(status.Host))
		} else {
			failedLocalPorts = append(failedLocalPorts, int(forward.LocalPort))
		}
		if fix == "" {
			needsPerPortStatus = true
		} else if !slices.Contains(fixes, fix) {
			fixes = append(fixes, fix)
		}
	}
	if len(failedLocalPorts) == 0 && len(failedRemotePorts) == 0 {
		return okDoctorCheck("forwards", fmt.Sprintf("%d forward(s), none failed", len(status.Forwards)))
	}
	if needsPerPortStatus {
		fixes = append([]string{"Run ssh-forward status for the per-port issue."}, fixes...)
	}
	return failedDoctorCheck("forwards", failedForwardDetail(failedLocalPorts, failedRemotePorts), strings.Join(fixes, " "))
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
