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
	var builder strings.Builder
	fmt.Fprintf(&builder, "Host: %s — %s\n", host.Alias, host.Connection)
	if host.ConnectionDiagnostic != "" {
		fmt.Fprintf(&builder, "  diagnostic: %s\n", host.ConnectionDiagnostic)
	}
	fmt.Fprintf(&builder, "Discovery: %s (baseline %v, scanner v%d)\n",
		host.Discovery.State, host.Discovery.BaselineEstablished, host.Discovery.ScannerVersion)
	if host.Discovery.Diagnostic != "" {
		fmt.Fprintf(&builder, "  diagnostic: %s\n", host.Discovery.Diagnostic)
	}
	if host.PolicyDiagnostic != "" {
		fmt.Fprintf(&builder, "Policies: %s\n", host.PolicyDiagnostic)
	}

	if len(host.Forwards) != 0 {
		builder.WriteString("Forwards:\n")
		for _, forward := range host.Forwards {
			fmt.Fprintf(&builder, "  %s → %s:%d (local %d)\n",
				forward.ID, forward.RemoteFamily, forward.RemotePort, forward.AllocatedLocalPort)
		}
	}

	var policies []core.ForwardingPolicy
	if a.PolicyReader != nil {
		policies, _ = a.PolicyReader.Read()
	}
	if ports := newRemotePorts(host, policies); len(ports) != 0 {
		fmt.Fprintf(&builder, "New remote ports: %s (ssh-forward add PORT)\n", strings.Join(ports, ", "))
	}
	if len(host.LocalPortConflicts) != 0 {
		builder.WriteString("Local port conflicts:\n")
		for _, conflict := range host.LocalPortConflicts {
			fmt.Fprintf(&builder, "  %s %s:%d\n", conflict.BindScope, conflict.RemoteFamily, conflict.RemotePort)
		}
	}
	_, err := io.WriteString(a.Options.Stdout, builder.String())
	return err
}

// newRemotePorts lists Available ports from the shared list module: observed,
// not an Active Forward, not a Local Port Conflict, and not Ignore.
func newRemotePorts(host *core.HostSnapshot, policies []core.ForwardingPolicy) []string {
	lists := present.FromSnapshot(host, nil, policies)
	ports := make([]string, 0, len(lists.Available))
	seen := make(map[uint16]struct{})
	for _, row := range lists.Available {
		if row.Reason == present.ReasonIgnored {
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
	switch {
	case dir != "" && adding && changed:
		fmt.Fprintf(a.Options.Stdout, "added directory %s\n", dir)
	case dir != "" && adding:
		fmt.Fprintf(a.Options.Stdout, "already added directory %s\n", dir)
	case dir != "" && changed:
		fmt.Fprintf(a.Options.Stdout, "removed directory %s\n", dir)
	case adding && changed:
		fmt.Fprintf(a.Options.Stdout, "added port %d\n", port)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "already added port %d\n", port)
	default:
		fmt.Fprintf(a.Options.Stdout, "removed port %d\n", port)
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
