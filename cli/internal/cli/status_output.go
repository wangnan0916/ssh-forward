package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"golang.org/x/term"

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

// forwardJSONStatus is the sole compatibility boundary for public status JSON.
// IPC uses core.Status directly; legacy same-port imports retain their port key.
func forwardJSONStatus(forward core.ForwardStatus) any {
	output := map[string]any{"state": forward.State}
	if forward.Diagnostic != "" {
		output["diagnostic"] = forward.Diagnostic
	}
	if forward.Direction == core.LocalToRemote {
		forward.PreferredRemotePort = cmp.Or(forward.PreferredRemotePort, forward.RemotePort)
		output["direction"], output["kind"] = core.LocalToRemote, "published"
		output["local_port"], output["remote_port"] = forward.LocalPort, forward.RemotePort
		output["preferred_remote_port"] = forward.PreferredRemotePort
		return output
	}
	if forward.Automatic {
		output["automatic"] = true
	}
	forward.PreferredLocalPort = cmp.Or(forward.PreferredLocalPort, forward.RemotePort)
	forward.LocalPort = cmp.Or(forward.LocalPort, forward.PreferredLocalPort)
	if forward.PreferredLocalPort == forward.RemotePort && forward.LocalPort == forward.RemotePort {
		output["port"] = forward.RemotePort
		return output
	}
	output["remote_port"], output["local_port"] = forward.RemotePort, forward.LocalPort
	output["preferred_local_port"] = forward.PreferredLocalPort
	if forward.AllowFallback {
		output["allow_fallback"] = true
	}
	return output
}

// watchScreen replaces the visible status on an interactive terminal.
// JSON and non-terminal output stay an append-only stream.
type watchScreen struct {
	writer io.Writer
	active bool
}

func newWatchScreen(writer io.Writer, jsonOutput bool) watchScreen {
	file, ok := writer.(*os.File)
	return watchScreen{writer: writer, active: ok && !jsonOutput && term.IsTerminal(int(file.Fd()))}
}

func (s watchScreen) enter() error {
	if !s.active {
		return nil
	}
	_, err := io.WriteString(s.writer, "\x1b[?25l")
	return err
}

func (s watchScreen) clear() error {
	if !s.active {
		return nil
	}
	_, err := io.WriteString(s.writer, "\x1b[H\x1b[2J")
	return err
}

func (s watchScreen) leave() {
	if s.active {
		_, _ = io.WriteString(s.writer, "\x1b[?25h")
	}
}

func (a *App) runWatch(ctx context.Context, jsonOutput bool) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	screen := newWatchScreen(a.Options.Stdout, jsonOutput)
	if err := screen.enter(); err != nil {
		return err
	}
	defer screen.leave()
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
			if err := screen.clear(); err != nil {
				return err
			}
			if !screen.active && !first && !jsonOutput {
				if _, err := fmt.Fprintln(a.Options.Stdout); err != nil {
					return err
				}
			}
			if err := a.writeStatuses(status, jsonOutput); err != nil {
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
