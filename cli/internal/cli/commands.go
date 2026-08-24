package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *App) writeStatusHuman(status core.Status) error {
	fmt.Fprintf(a.Options.Stdout, "Host: %s — discovery %s\n", status.Host, status.Discovery.State)
	if status.Discovery.Diagnostic != "" {
		fmt.Fprintf(a.Options.Stdout, "Discovery: %s\n", diagnosticText(status.Discovery.Diagnostic))
	}
	remembered := make(map[uint16]struct{}, len(status.Forwards))
	listenersByPort := make(map[uint16]core.Listener, len(status.Listeners))
	for _, listener := range status.Listeners {
		listenersByPort[listener.Port] = listener
	}
	for _, forward := range status.Forwards {
		remembered[forward.Port] = struct{}{}
	}
	for _, state := range []core.ForwardState{core.ForwardActive, core.ForwardStarting, core.ForwardFailed} {
		var rows []core.ForwardStatus
		for _, forward := range status.Forwards {
			if forward.State == state {
				rows = append(rows, forward)
			}
		}
		if len(rows) == 0 {
			continue
		}
		heading := map[core.ForwardState]string{
			core.ForwardActive: "Forwards:", core.ForwardStarting: "Starting:",
			core.ForwardFailed: "Needs attention:",
		}[state]
		fmt.Fprintln(a.Options.Stdout, heading)
		for _, row := range rows {
			listener := listenersByPort[row.Port]
			switch state {
			case core.ForwardActive:
				fmt.Fprintf(a.Options.Stdout, "  %5d → 127.0.0.1:%d%s\n", row.Port, row.Port, listenerMetadata(listener))
			case core.ForwardFailed:
				fmt.Fprintf(a.Options.Stdout, "  %5d  %s\n", row.Port, diagnosticText(row.Diagnostic))
			default:
				fmt.Fprintf(a.Options.Stdout, "  %5d\n", row.Port)
			}
		}
	}
	var available []core.Listener
	for _, listener := range status.Listeners {
		if _, found := remembered[listener.Port]; !found {
			available = append(available, listener)
		}
	}
	if len(available) != 0 {
		fmt.Fprintln(a.Options.Stdout, "Available:")
		for _, listener := range available {
			fmt.Fprintf(a.Options.Stdout, "  %5d%s\n", listener.Port, listenerMetadata(listener))
		}
	}
	if len(status.Forwards) == 0 && len(available) == 0 && status.Discovery.State == core.DiscoveryActive {
		fmt.Fprintln(a.Options.Stdout, "No loopback TCP listeners found.")
	}
	return nil
}

func listenerMetadata(listener core.Listener) string {
	switch {
	case listener.App == "" && listener.WorkingDirectory == "":
		return ""
	case listener.App == "":
		return "  " + listener.WorkingDirectory
	case listener.WorkingDirectory == "":
		return "  " + listener.App
	default:
		return "  " + listener.App + "  " + listener.WorkingDirectory
	}
}

func diagnosticText(diagnostic string) string {
	switch diagnostic {
	case "invalid_alias":
		return "SSH does not know this host alias."
	case "authentication_failed":
		return "SSH authentication failed."
	case "host_key_failed":
		return "SSH host key verification failed."
	case "local_port_conflict":
		return "the same local port is already in use"
	case "transport_unavailable":
		return "SSH connection unavailable"
	case "discovery_invalid":
		return "the remote listener scan returned invalid data"
	case "forward_start_timeout":
		return "OpenSSH did not open the local port in time"
	default:
		return diagnostic
	}
}

func (a *App) writeJSON(value any) error {
	return json.NewEncoder(a.Options.Stdout).Encode(value)
}

func (a *App) writeRemember(jsonOutput, adding, changed bool, host string, port uint16) error {
	if jsonOutput {
		key := "removed"
		if adding {
			key = "added"
		}
		return a.writeJSON(map[string]any{key: changed, "host": host, "port": port})
	}
	switch {
	case adding && changed:
		fmt.Fprintf(a.Options.Stdout, "Remembered %d for %s.\n", port, host)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Already remembered %d for %s.\n", port, host)
	default:
		fmt.Fprintf(a.Options.Stdout, "Forgot %d for %s.\n", port, host)
	}
	return nil
}

func (a *App) runWatch(ctx context.Context, jsonOutput bool) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previous core.Status
	first := true
	for {
		status, err := a.Manager.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if first || !reflect.DeepEqual(status, previous) {
			if !first && !jsonOutput {
				fmt.Fprintln(a.Options.Stdout)
			}
			if jsonOutput {
				err = a.writeJSON(status)
			} else {
				err = a.writeStatusHuman(status)
			}
			if err != nil {
				return err
			}
			previous, first = status, false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
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
	selected := a.defaultHostAlias()
	for _, host := range hosts {
		suffix := ""
		if host == selected {
			suffix = "  (default)"
		}
		fmt.Fprintln(a.Options.Stdout, "  "+host+suffix)
	}
	if len(hosts) == 0 {
		fmt.Fprintln(a.Options.Stdout, "No Host aliases found.")
	} else if selected == "" {
		fmt.Fprintln(a.Options.Stdout, "Pin one: ssh-forward default ALIAS")
	}
	return nil
}

func (a *App) defaultHostAlias() string {
	host, _ := app.PinnedHost(a.Options.ConfigPath)
	return host
}

func (a *App) runShowDefault() error {
	host, err := app.PinnedHost(a.Options.ConfigPath)
	if errors.Is(err, app.ErrNoHost) {
		fmt.Fprintln(a.Options.Stdout, "No default host. Pin one with: ssh-forward default ALIAS")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host: %s\n", host)
	return nil
}

func (a *App) runSetDefault(alias string) error {
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
	if err != nil {
		return err
	}
	if !slices.Contains(hosts, alias) {
		return UsageError(fmt.Errorf("%s is not a literal Host alias in your SSH config", alias))
	}
	if err := app.SetDefaultHost(a.Options.ConfigPath, alias); err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host set to %s\n", alias)
	return nil
}
