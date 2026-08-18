package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

func (a *App) writeStatusHuman(snapshot core.Snapshot) error {
	host := snapshot.Host
	var builder strings.Builder
	fmt.Fprintf(&builder, "Host: %s — %s\n", host.Alias, host.Connection)
	fmt.Fprintf(&builder, "Discovery: %s (baseline %v, scanner v%d)\n",
		host.Discovery.State, host.Discovery.BaselineEstablished, host.Discovery.ScannerVersion)
	if host.Discovery.Diagnostic != "" {
		fmt.Fprintf(&builder, "  diagnostic: %s\n", host.Discovery.Diagnostic)
	}

	if len(host.Forwards) != 0 {
		builder.WriteString("Forwards:\n")
		for _, forward := range host.Forwards {
			fmt.Fprintf(&builder, "  %s → %s:%d (local %d)\n",
				forward.ID, forward.RemoteFamily, forward.RemotePort, forward.AllocatedLocalPort)
		}
	}

	if ports := newRemotePorts(host); len(ports) != 0 {
		fmt.Fprintf(&builder, "New remote ports: %s (ssh-forward add PORT)\n", strings.Join(ports, ", "))
	}
	if len(host.LocalPortConflicts) != 0 {
		builder.WriteString("Local port conflicts:\n")
		for _, conflict := range host.LocalPortConflicts {
			fmt.Fprintf(&builder, "  %s %s:%d\n", conflict.BindScope, conflict.RemoteFamily, conflict.RemotePort)
		}
	}
	_, err := io.WriteString(a.Stdout, builder.String())
	return err
}

// newRemotePorts lists observed remote ports that have no Active Forward,
// in observation order, once each.
func newRemotePorts(host *core.HostSnapshot) []string {
	forwarded := make(map[uint16]struct{}, len(host.Forwards))
	for _, forward := range host.Forwards {
		forwarded[forward.RemotePort] = struct{}{}
	}
	seen := make(map[uint16]struct{})
	ports := make([]string, 0)
	for _, observation := range host.ListenerObservations {
		if _, ok := forwarded[observation.RemotePort]; ok {
			continue
		}
		if _, ok := seen[observation.RemotePort]; ok {
			continue
		}
		seen[observation.RemotePort] = struct{}{}
		ports = append(ports, strconv.Itoa(int(observation.RemotePort)))
	}
	return ports
}

func (a *App) writeSnapshotJSON(snapshot core.Snapshot) error {
	encoded, err := jsonrpc.MarshalSnapshot(snapshot)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, string(encoded))
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
		fmt.Fprintln(a.Stdout, string(encoded))
		return nil
	}
	switch {
	case dir != "" && adding && changed:
		fmt.Fprintf(a.Stdout, "added directory %s\n", dir)
	case dir != "" && adding:
		fmt.Fprintf(a.Stdout, "already added directory %s\n", dir)
	case dir != "" && changed:
		fmt.Fprintf(a.Stdout, "removed directory %s\n", dir)
	case adding && changed:
		fmt.Fprintf(a.Stdout, "added port %d\n", port)
	case adding:
		fmt.Fprintf(a.Stdout, "already added port %d\n", port)
	default:
		fmt.Fprintf(a.Stdout, "removed port %d\n", port)
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
