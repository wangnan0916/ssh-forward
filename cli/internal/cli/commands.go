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
	reliable := true
	if a.PolicyReader != nil {
		policies, reliable, _ = a.PolicyReader.Effective()
	}
	doc := present.NewDocument(host, policies, reliable)
	var addable []uint16
	if reliable {
		addable = present.AddablePorts(host, doc.Lists)
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "Host: %s — %s\n", doc.Chrome.Alias, doc.Chrome.Connection)
	if note := connectionNote(doc.Chrome); note != "" {
		fmt.Fprintf(&builder, "%s\n", note)
	}
	if note := discoveryNote(doc.Chrome); note != "" {
		fmt.Fprintf(&builder, "%s\n", note)
	}
	if note := policyNote(doc.Chrome.PolicyDiagnostic); note != "" {
		fmt.Fprintf(&builder, "%s\n", note)
	}

	if len(doc.Lists.Active) != 0 {
		builder.WriteString("Forwards:\n")
		for _, row := range doc.Lists.Active {
			line := fmt.Sprintf("  %d → 127.0.0.1:%d", row.Port, row.Local)
			if row.Exe != "" {
				line += "  " + row.Exe
			}
			builder.WriteString(line + "\n")
		}
	}
	if len(doc.Lists.Waiting) != 0 {
		builder.WriteString("Waiting:\n")
		for _, row := range doc.Lists.Waiting {
			fmt.Fprintf(&builder, "  %d  (nothing listening yet)\n", row.Port)
		}
	}
	if len(addable) != 0 {
		builder.WriteString("Available:\n")
		for _, port := range addable {
			fmt.Fprintf(&builder, "  %d  ssh-forward add %d\n", port, port)
		}
	}
	if len(doc.Lists.Attention) != 0 {
		builder.WriteString("Needs attention:\n")
		for _, row := range doc.Lists.Attention {
			fmt.Fprintf(&builder, "  %d  could not bind a local port\n", row.Port)
		}
	}
	if doc.Chrome.Connection == string(core.ConnectionConnected) &&
		len(doc.Lists.Active) == 0 && len(doc.Lists.Waiting) == 0 && len(addable) == 0 && len(doc.Lists.Attention) == 0 {
		builder.WriteString("No ports forwarded yet. Remember one with: ssh-forward add PORT\n")
	}
	_, err := io.WriteString(a.Options.Stdout, builder.String())
	return err
}

func connectionNote(chrome present.Chrome) string {
	if chrome.Connection == string(core.ConnectionConnecting) {
		return "Still opening the SSH session."
	}
	switch chrome.ConnectionDiagnostic {
	case "":
		return ""
	case "invalid_alias":
		return "SSH does not know this host alias. Check ~/.ssh/config or pass --host."
	case "authentication_failed":
		return "SSH authentication failed."
	case "host_key_failed":
		return "SSH host key verification failed."
	default:
		return chrome.ConnectionDiagnostic
	}
}

func discoveryNote(chrome present.Chrome) string {
	if chrome.Connection == string(core.ConnectionConnecting) &&
		(chrome.Discovery == string(core.DiscoveryStopped) || chrome.Discovery == string(core.DiscoveryStarting)) {
		return ""
	}
	switch chrome.Discovery {
	case string(core.DiscoveryHealthy):
		return ""
	case string(core.DiscoveryStopped):
		return "Discovery has not started."
	case string(core.DiscoveryStarting):
		return "Discovery is starting."
	case string(core.DiscoveryDegraded):
		if chrome.DiscoveryDiagnostic == "process_metadata_unavailable" {
			return "Process names are unavailable on this host."
		}
		if chrome.DiscoveryDiagnostic != "" {
			return chrome.DiscoveryDiagnostic
		}
		return "Discovery is degraded."
	case string(core.DiscoveryFailed):
		if chrome.DiscoveryDiagnostic != "" {
			return "Discovery failed: " + chrome.DiscoveryDiagnostic
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
