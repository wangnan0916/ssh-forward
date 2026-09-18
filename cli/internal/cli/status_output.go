package cli

import (
	"context"
	"fmt"
	"reflect"
	"time"

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

type mappedForwardJSONOutput struct {
	RemotePort         uint16            `json:"remote_port"`
	PreferredLocalPort uint16            `json:"preferred_local_port"`
	LocalPort          uint16            `json:"local_port"`
	State              core.ForwardState `json:"state"`
	Diagnostic         string            `json:"diagnostic,omitempty"`
	Automatic          bool              `json:"automatic,omitempty"`
	AllowFallback      bool              `json:"allow_fallback,omitempty"`
}

type publishedForwardJSONOutput struct {
	Direction           core.ForwardDirection `json:"direction"`
	LocalPort           uint16                `json:"local_port"`
	PreferredRemotePort uint16                `json:"preferred_remote_port"`
	RemotePort          uint16                `json:"remote_port"`
	State               core.ForwardState     `json:"state"`
	Diagnostic          string                `json:"diagnostic,omitempty"`
	Kind                string                `json:"kind"`
}

func statusJSON(status core.Status) statusJSONOutput {
	var forwards []any
	if status.Forwards != nil {
		forwards = make([]any, len(status.Forwards))
		for index, forward := range status.Forwards {
			forwards[index] = forwardJSONStatus(forward)
		}
	}
	return statusJSONOutput{
		Host: status.Host, Discovery: status.Discovery,
		Listeners: status.Listeners, Forwards: forwards,
		WorkingDirectoryRules: status.WorkingDirectoryRules,
	}
}

func (a *App) writeStatuses(statuses []core.Status, jsonOutput bool) error {
	if jsonOutput {
		if a.Options.HostFlag != "" && len(statuses) == 1 {
			return a.writeJSON(statusJSON(statuses[0]))
		}
		output := make([]statusJSONOutput, 0, len(statuses))
		for _, status := range statuses {
			output = append(output, statusJSON(status))
		}
		return a.writeJSON(output)
	}
	if len(statuses) == 0 {
		_, err := fmt.Fprintln(a.Options.Stdout, "No hosts are managed.")
		return err
	}
	for index, status := range statuses {
		if index > 0 {
			if _, err := fmt.Fprintln(a.Options.Stdout); err != nil {
				return err
			}
		}
		if err := a.writeStatusHuman(status); err != nil {
			return err
		}
	}
	return nil
}

func forwardJSONStatus(forward core.ForwardStatus) any {
	if forward.Direction == core.LocalToRemote {
		preferredRemotePort := forward.PreferredRemotePort
		if preferredRemotePort == 0 {
			preferredRemotePort = forward.RemotePort
		}
		return publishedForwardJSONOutput{
			Direction: core.LocalToRemote, LocalPort: forward.LocalPort,
			PreferredRemotePort: preferredRemotePort, RemotePort: forward.RemotePort,
			State: forward.State, Diagnostic: forward.Diagnostic, Kind: "published",
		}
	}
	preferredLocalPort := forward.PreferredLocalPort
	if preferredLocalPort == 0 {
		preferredLocalPort = forward.RemotePort
	}
	localPort := forward.LocalPort
	if localPort == 0 {
		localPort = preferredLocalPort
	}
	if preferredLocalPort != forward.RemotePort || localPort != forward.RemotePort {
		return mappedForwardJSONOutput{
			RemotePort: forward.RemotePort, PreferredLocalPort: preferredLocalPort,
			LocalPort: localPort, State: forward.State, Diagnostic: forward.Diagnostic,
			Automatic: forward.Automatic, AllowFallback: forward.AllowFallback,
		}
	}
	return legacyForwardJSONOutput{
		Port: forward.RemotePort, State: forward.State,
		Diagnostic: forward.Diagnostic, Automatic: forward.Automatic,
	}
}

func (a *App) runWatch(ctx context.Context, jsonOutput bool) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previous []core.Status
	first := true
	for {
		status, err := a.readStatuses(ctx)
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
			err = a.writeStatuses(status, jsonOutput)
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
