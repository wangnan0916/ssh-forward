package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/present"
	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

func (a *App) writeStatusHuman(snap core.Snapshot) error {
	host := snap.Host
	var policies []core.ForwardingPolicy
	if a.PolicyReader != nil {
		policies, _ = a.PolicyReader.Read()
	}
	lists := present.FromSnapshot(host, core.SimpleAutoForwardPorts(policies), policies)
	ports := newRemotePorts(host, lists)

	var builder strings.Builder
	fmt.Fprintf(&builder, "Host: %s — %s\n", host.Alias, host.Connection)
	if note := connectionNote(host); note != "" {
		fmt.Fprintf(&builder, "%s\n", note)
	}
	if note := discoveryNote(host); note != "" {
		fmt.Fprintf(&builder, "%s\n", note)
	}
	if note := policyNote(host.PolicyDiagnostic); note != "" {
		fmt.Fprintf(&builder, "%s\n", note)
	}

	if len(lists.Active) != 0 {
		builder.WriteString("Forwards:\n")
		for _, row := range lists.Active {
			line := fmt.Sprintf("  %d → 127.0.0.1:%d", row.Port, row.Local)
			if row.Exe != "" {
				line += "  " + row.Exe
			}
			builder.WriteString(line + "\n")
		}
	}
	if len(lists.Waiting) != 0 {
		builder.WriteString("Waiting:\n")
		for _, row := range lists.Waiting {
			fmt.Fprintf(&builder, "  %d  (nothing listening yet)\n", row.Port)
		}
	}
	if len(ports) != 0 {
		builder.WriteString("Available:\n")
		for _, port := range ports {
			fmt.Fprintf(&builder, "  %s  ssh-forward add %s\n", port, port)
		}
	}
	if len(lists.Attention) != 0 {
		builder.WriteString("Needs attention:\n")
		for _, row := range lists.Attention {
			fmt.Fprintf(&builder, "  %d  could not bind a local port\n", row.Port)
		}
	}
	if host.Connection == core.ConnectionConnected &&
		len(lists.Active) == 0 && len(lists.Waiting) == 0 && len(ports) == 0 && len(lists.Attention) == 0 {
		builder.WriteString("No ports forwarded yet. Remember one with: ssh-forward add PORT\n")
	}
	_, err := io.WriteString(a.Options.Stdout, builder.String())
	return err
}

func connectionNote(host *core.HostSnapshot) string {
	if host.Connection == core.ConnectionConnecting {
		return "Still opening the SSH session."
	}
	switch host.ConnectionDiagnostic {
	case "":
		return ""
	case "invalid_alias":
		return "SSH does not know this host alias. Check ~/.ssh/config or pass --host."
	case "authentication_failed":
		return "SSH authentication failed."
	case "host_key_failed":
		return "SSH host key verification failed."
	default:
		return host.ConnectionDiagnostic
	}
}

func discoveryIdle(host *core.HostSnapshot) bool {
	return host.Discovery.State == core.DiscoveryStopped || host.Discovery.State == core.DiscoveryStarting
}

func discoveryNote(host *core.HostSnapshot) string {
	if host.Connection == core.ConnectionConnecting && discoveryIdle(host) {
		return ""
	}
	switch host.Discovery.State {
	case core.DiscoveryHealthy:
		return ""
	case core.DiscoveryStopped:
		return "Discovery has not started."
	case core.DiscoveryStarting:
		return "Discovery is starting."
	case core.DiscoveryDegraded:
		if host.Discovery.Diagnostic == "process_metadata_unavailable" {
			return "Process names are unavailable on this host."
		}
		if host.Discovery.Diagnostic != "" {
			return host.Discovery.Diagnostic
		}
		return "Discovery is degraded."
	case core.DiscoveryFailed:
		if host.Discovery.Diagnostic != "" {
			return "Discovery failed: " + host.Discovery.Diagnostic
		}
		return "Discovery failed."
	default:
		return ""
	}
}

func policyNote(diagnostic string) string {
	switch diagnostic {
	case "":
		return ""
	case "policies_file_invalid":
		return "policies.jsonc is unreadable; last valid rules are still in effect."
	default:
		return diagnostic
	}
}

// newRemotePorts lists loopback Available ports that status offers to add:
// not Ignore, not already matched Auto-forward. Wildcard listeners stay on
// the host; WebUI still shows every Available row.
func newRemotePorts(host *core.HostSnapshot, lists present.Lists) []string {
	ports := make([]string, 0, len(lists.Available))
	seen := make(map[uint16]struct{})
	for _, row := range lists.Available {
		if row.Reason == present.ReasonIgnored || row.Reason == present.ReasonAutoForward {
			continue
		}
		if !loopbackPort(host, row.Port) {
			continue
		}
		if _, ok := seen[row.Port]; ok {
			continue
		}
		seen[row.Port] = struct{}{}
		ports = append(ports, strconv.Itoa(int(row.Port)))
	}
	return ports
}

func loopbackPort(host *core.HostSnapshot, port uint16) bool {
	for _, observation := range host.ListenerObservations {
		if observation.RemotePort == port && observation.BindScope == core.BindLoopback {
			return true
		}
	}
	return false
}

func (a *App) writeSnapshotJSON(snap core.Snapshot) error {
	encoded, err := snapshot.Marshal(snap)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Options.Stdout, string(encoded))
	return nil
}

func (a *App) writeRemember(jsonOutput, adding, changed bool, port uint16, dir string) error {
	if jsonOutput {
		payload := map[string]any{}
		if adding {
			payload["added"] = changed
		} else {
			payload["removed"] = changed
		}
		if dir != "" {
			payload["directory"] = dir
		} else {
			payload["port"] = port
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Options.Stdout, string(encoded))
		return nil
	}
	target := strconv.Itoa(int(port))
	if dir != "" {
		target = "directory " + dir
	}
	switch {
	case adding && changed:
		fmt.Fprintf(a.Options.Stdout, "Remembered %s. Check with: ssh-forward status\n", target)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Already remembered %s.\n", target)
	default:
		fmt.Fprintf(a.Options.Stdout, "Forgot %s.\n", target)
	}
	return nil
}

// parsePort reports whether the argument is a plain port number.
func parsePort(text string) (uint16, bool) {
	port, err := strconv.ParseUint(text, 10, 16)
	if err != nil || port == 0 {
		return 0, false
	}
	return uint16(port), true
}
