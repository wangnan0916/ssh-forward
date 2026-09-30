package cli

import (
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

func (a *App) writeStatuses(statuses []core.Status, jsonOutput bool) error {
	if jsonOutput {
		if a.Options.HostFlag != "" && len(statuses) == 1 {
			return a.writeJSON(statuses[0])
		}
		return a.writeJSON(statuses)
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

func (s watchScreen) write(sequence string) error {
	if !s.active {
		return nil
	}
	_, err := io.WriteString(s.writer, sequence)
	return err
}

func (s watchScreen) enter() error { return s.write("\x1b[?25l") }
func (s watchScreen) clear() error { return s.write("\x1b[H\x1b[2J") }
func (s watchScreen) leave()       { _ = s.write("\x1b[?25h") }

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
