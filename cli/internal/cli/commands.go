package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *App) writeStatusHuman(snap core.Snapshot) error {
	host := snap.Host
	var policies []core.ForwardingPolicy
	reliable := true
	if a.PolicyReader != nil {
		policies, reliable, _ = a.PolicyReader.Effective()
	}
	doc := NewDocument(host, policies, reliable)
	text := formatHumanStatus(doc)
	_, err := io.WriteString(a.Options.Stdout, text)
	return err
}

func formatHumanStatus(doc Document) string {
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
	if len(doc.Addable) != 0 {
		builder.WriteString("Available:\n")
		for _, port := range doc.Addable {
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
		len(doc.Lists.Active) == 0 && len(doc.Lists.Waiting) == 0 && len(doc.Addable) == 0 && len(doc.Lists.Attention) == 0 {
		builder.WriteString("No ports forwarded yet. Remember one with: ssh-forward add PORT\n")
	}
	return builder.String()
}

func connectionNote(chrome Chrome) string {
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

func discoveryNote(chrome Chrome) string {
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

func (a *App) writeJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return a.writeJSONLine(encoded)
}

func (a *App) writeSnapshotJSON(snap core.Snapshot) error {
	return a.writeJSON(snap)
}

func (a *App) writeJSONLine(encoded []byte) error {
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
		return a.writeJSON(payload)
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

func (a *App) runWatch(ctx context.Context, jsonOutput bool) error {
	stream, err := a.Manager.Watch(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	first := true
	for {
		snap, err := stream.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if jsonOutput {
			if err := a.writeSnapshotJSON(snap); err != nil {
				return err
			}
			continue
		}
		if !first {
			fmt.Fprintln(a.Options.Stdout)
		}
		first = false
		if err := a.writeStatusHuman(snap); err != nil {
			return err
		}
	}
}

func (a *App) runHostList(jsonOutput bool) error {
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
	if err != nil {
		return err
	}
	if jsonOutput {
		return a.writeJSON(hosts)
	}
	fmt.Fprintln(a.Options.Stdout, "Hosts in ~/.ssh/config:")
	if len(hosts) == 0 {
		fmt.Fprintln(a.Options.Stdout, "No Host aliases in ~/.ssh/config. Add a Host block, then: ssh-forward default ALIAS")
		return nil
	}
	selected := a.defaultHostAlias()
	for _, host := range hosts {
		if host == selected {
			fmt.Fprintf(a.Options.Stdout, "  %s  (default)\n", host)
			continue
		}
		fmt.Fprintf(a.Options.Stdout, "  %s\n", host)
	}
	if selected == "" {
		fmt.Fprintln(a.Options.Stdout, "Pin one: ssh-forward default ALIAS")
	}
	return nil
}

func (a *App) defaultHostAlias() string {
	host, err := app.PinnedHost(a.Options.ConfigPath)
	if err != nil {
		return ""
	}
	return host
}

func (a *App) runShowDefault() error {
	host, err := app.PinnedHost(a.Options.ConfigPath)
	if errors.Is(err, app.ErrNoHost) {
		fmt.Fprintln(a.Options.Stdout, "No default host.")
		fmt.Fprintln(a.Options.Stdout, "List aliases: ssh-forward host")
		fmt.Fprintln(a.Options.Stdout, "Then pin one: ssh-forward default ALIAS")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host: %s\n", host)
	return nil
}

func (a *App) runSetDefault(alias string) error {
	path := a.Options.ConfigPath
	if path == "" {
		return fmt.Errorf("no config path is configured")
	}
	if err := app.SetDefaultHost(path, alias); err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host set to %s\n", alias)
	return nil
}

func (a *App) runPolicyList(jsonOutput bool) error {
	if a.Options.PoliciesPath == "" {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	policies, reliable, err := a.PolicyReader.Effective()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if reliable {
			fmt.Fprintf(a.Options.Stderr, "warning: %v; showing the last valid policies\n", err)
		} else {
			fmt.Fprintf(a.Options.Stderr, "warning: %v; this process has no last-valid policies\n", err)
		}
	}
	if jsonOutput {
		encoded, err := app.MarshalPolicies(policies)
		if err != nil {
			return err
		}
		return a.writeJSONLine(encoded)
	}
	return a.writePolicyListHuman(policies)
}

func (a *App) writePolicyListHuman(policies []core.ForwardingPolicy) error {
	if len(policies) == 0 {
		fmt.Fprintln(a.Options.Stdout, "Nothing remembered yet. ssh-forward add PORT")
		return nil
	}
	var remembered []string
	var other []core.ForwardingPolicy
	for _, policy := range policies {
		if port, dir, ok := core.DescribeSimpleAutoForward(policy); ok {
			if dir != "" {
				remembered = append(remembered, "  "+dir)
			} else {
				remembered = append(remembered, fmt.Sprintf("  %d", port))
			}
			continue
		}
		other = append(other, policy)
	}
	if len(remembered) != 0 {
		fmt.Fprintln(a.Options.Stdout, "Remembered:")
		for _, line := range remembered {
			fmt.Fprintln(a.Options.Stdout, line)
		}
	}
	if len(other) != 0 {
		if len(remembered) != 0 {
			fmt.Fprintln(a.Options.Stdout)
		}
		fmt.Fprintln(a.Options.Stdout, "Other policies:")
		for _, policy := range other {
			action := strings.ReplaceAll(string(policy.Action), "_", "-")
			fmt.Fprintf(a.Options.Stdout, "  %s  %s\n", policy.ID, action)
		}
	}
	return nil
}
