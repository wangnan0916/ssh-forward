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
	"github.com/wangnan0916/ssh-forward/cli/internal/statusview"
)

func (a *App) writeStatusHuman(status core.Status) error {
	return statusview.Render(a.Options.Stdout, status, statusViewOptions(a.Options.Stdout))
}

type statusJSONOutput struct {
	Host                  core.HostAlias       `json:"host"`
	Discovery             core.DiscoveryStatus `json:"discovery"`
	Listeners             []core.Listener      `json:"listeners"`
	Forwards              []any                `json:"forwards"`
	WorkingDirectoryRules []string             `json:"working_directory_rules,omitempty"`
}

type legacyForwardJSONOutput struct {
	Port       uint16            `json:"port"`
	State      core.ForwardState `json:"state"`
	Diagnostic string            `json:"diagnostic,omitempty"`
	Automatic  bool              `json:"automatic,omitempty"`
}

func (a *App) writeStatusJSON(status core.Status) error {
	var forwards []any
	if status.Forwards != nil {
		forwards = make([]any, len(status.Forwards))
		for index, forward := range status.Forwards {
			forwards[index] = forwardJSONStatus(forward)
		}
	}
	return a.writeJSON(statusJSONOutput{
		Host: status.Host, Discovery: status.Discovery,
		Listeners: status.Listeners, Forwards: forwards,
		WorkingDirectoryRules: status.WorkingDirectoryRules,
	})
}

func forwardJSONStatus(forward core.ForwardStatus) any {
	preferredLocalPort := forward.PreferredLocalPort
	if preferredLocalPort == 0 {
		preferredLocalPort = forward.RemotePort
	}
	localPort := forward.LocalPort
	if localPort == 0 {
		localPort = preferredLocalPort
	}
	if preferredLocalPort != forward.RemotePort || localPort != forward.RemotePort {
		return forward
	}
	return legacyForwardJSONOutput{
		Port: forward.RemotePort, State: forward.State,
		Diagnostic: forward.Diagnostic, Automatic: forward.Automatic,
	}
}

func (a *App) writeJSON(value any) error {
	return json.NewEncoder(a.Options.Stdout).Encode(value)
}

func (a *App) writeRemember(
	jsonOutput, adding, changed bool,
	host string,
	forward core.RememberedForward,
) error {
	if jsonOutput {
		key := "removed"
		if adding {
			key = "added"
		}
		output := map[string]any{
			key:           changed,
			"host":        host,
			"remote_port": forward.RemotePort,
		}
		if adding {
			output["local_port"] = forward.LocalPort
			output["allow_fallback"] = forward.AllowFallback
		}
		return a.writeJSON(output)
	}
	switch {
	case adding && changed && forward.AllowFallback:
		fmt.Fprintf(
			a.Options.Stdout,
			"Remembered remote %d for %s (prefers 127.0.0.1:%d; falls back if busy).\n",
			forward.RemotePort, host, forward.LocalPort,
		)
	case adding && changed:
		fmt.Fprintf(
			a.Options.Stdout,
			"Remembered remote %d at 127.0.0.1:%d for %s.\n",
			forward.RemotePort, forward.LocalPort, host,
		)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Already remembered remote %d for %s.\n", forward.RemotePort, host)
	default:
		fmt.Fprintf(a.Options.Stdout, "Forgot remote %d for %s.\n", forward.RemotePort, host)
	}
	return nil
}

func (a *App) writeRememberWorkingDirectory(jsonOutput, adding, changed bool, host, pattern string) error {
	if jsonOutput {
		key := "removed"
		if adding {
			key = "added"
		}
		return a.writeJSON(map[string]any{key: changed, "host": host, "working_directory_rule": pattern})
	}
	switch {
	case adding && changed:
		fmt.Fprintf(a.Options.Stdout, "Remembered working-directory glob %s for %s.\n", pattern, host)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Already remembered working-directory glob %s for %s.\n", pattern, host)
	default:
		fmt.Fprintf(a.Options.Stdout, "Forgot working-directory glob %s for %s.\n", pattern, host)
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
				err = a.writeStatusJSON(status)
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
