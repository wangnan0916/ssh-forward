package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/ui"
)

var errUINotRunning = errors.New("WebUI is not running")

func (a *App) uiCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "ui",
		Short: "run the loopback WebUI",
		RunE: func(cmd *cobra.Command, args []string) error {
			return UsageError(fmt.Errorf("ui needs a subcommand (start, status, stop)"))
		},
	}
	start := &cobra.Command{
		Use:   "start",
		Short: "start the WebUI in the background",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUIStart(cmd.Context())
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "print the WebUI URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput, _ := cmd.Flags().GetBool("json")
			return a.runUIStatus(jsonOutput)
		},
	}
	status.Flags().Bool("json", false, "emit JSON")
	stop := &cobra.Command{
		Use:   "stop",
		Short: "stop the WebUI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUIStop()
		},
	}
	serve := &cobra.Command{
		Use:    "serve",
		Short:  "run the WebUI in the foreground",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUIServe(cmd.Context())
		},
	}
	command.AddCommand(start, status, stop, serve)
	return annotateSkipManager(command)
}

func (a *App) runUIStatus(jsonOutput bool) error {
	url, err := liveUIURL(a.Options.Layout)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoded, err := json.Marshal(map[string]string{"url": url})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Options.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintln(a.Options.Stdout, url)
	return nil
}

func (a *App) runUIStop() error {
	pid, err := app.ReadPIDFile(a.Options.Layout.UIPID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errUINotRunning
		}
		return err
	}
	if !app.PIDAlive(pid) {
		removeUIFiles(a.Options.Layout)
		return errUINotRunning
	}
	if err := app.TerminatePID(pid); err != nil {
		return fmt.Errorf("stop WebUI: %w", err)
	}
	removeUIFiles(a.Options.Layout)
	fmt.Fprintln(a.Options.Stdout, "stopped")
	return nil
}

func (a *App) runUIStart(ctx context.Context) error {
	if url, err := liveUIURL(a.Options.Layout); err == nil {
		a.announceUI(url)
		return nil
	}
	pid, err := app.ReadPIDFile(a.Options.Layout.UIPID)
	if err != nil || !app.PIDAlive(pid) {
		pid, err = spawnUI(a.Options)
		if err != nil {
			return err
		}
	}
	url, err := waitForUI(ctx, a.Options.Layout, 10*time.Second, pid)
	if err != nil {
		return err
	}
	a.announceUI(url)
	return nil
}

func (a *App) announceUI(url string) {
	fmt.Fprintln(a.Options.Stdout, url)
	_ = openBrowser(url)
}

func (a *App) runUIServe(ctx context.Context) error {
	if err := a.ensureSession(ctx); err != nil {
		return err
	}
	if a.PolicyReader == nil {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	token, err := ui.NewToken()
	if err != nil {
		return err
	}
	listener, err := ui.ListenLoopback()
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.MkdirAll(a.Options.Layout.Dir, 0o700); err != nil {
		return err
	}
	url := ui.PageURL(listener.Addr(), token)
	defer removeUIFiles(a.Options.Layout)
	if err := os.WriteFile(a.Options.Layout.UIPID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(a.Options.Layout.UIURL, []byte(url+"\n"), 0o600); err != nil {
		return err
	}
	handler := (&ui.Server{Manager: a.Manager, Ports: a.PolicyReader, Token: token}).Handler()
	return ui.Serve(ctx, listener, handler)
}

func liveUIURL(layout app.Layout) (string, error) {
	pid, err := app.ReadPIDFile(layout.UIPID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errUINotRunning
		}
		return "", err
	}
	if !app.PIDAlive(pid) {
		return "", errUINotRunning
	}
	raw, err := os.ReadFile(layout.UIURL)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errUINotRunning
		}
		return "", err
	}
	url := strings.TrimSpace(string(raw))
	if url == "" {
		return "", errUINotRunning
	}
	return url, nil
}

func removeUIFiles(layout app.Layout) {
	_ = os.Remove(layout.UIPID)
	_ = os.Remove(layout.UIURL)
}

func waitForUI(ctx context.Context, layout app.Layout, timeout time.Duration, childPID int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if url, err := liveUIURL(layout); err == nil {
			return url, nil
		}
		if childPID > 0 && !app.PIDAlive(childPID) {
			return "", uiStartError(layout, "WebUI failed to start")
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", uiStartError(layout, fmt.Sprintf("WebUI did not start within %s", timeout))
			}
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func uiStartError(layout app.Layout, fallback string) error {
	if line := lastUILogLine(layout); line != "" {
		return fmt.Errorf("WebUI failed to start: %s (see %q)", line, layout.UILog)
	}
	return fmt.Errorf("%s (see %q)", fallback, layout.UILog)
}

func lastUILogLine(layout app.Layout) string {
	raw, err := os.ReadFile(layout.UILog)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return strings.TrimPrefix(line, "ssh-forward: ")
		}
	}
	return ""
}

func spawnUI(opts app.Options) (int, error) {
	executable, err := app.ResolveSpawnBinary("SSH_FORWARD_UI_BINARY")
	if err != nil {
		return 0, err
	}
	var args []string
	if opts.HostFlag != "" {
		args = append(args, "--host", opts.HostFlag)
	}
	if opts.PoliciesPath != "" {
		args = append(args, "--policies", opts.PoliciesPath)
	}
	if opts.SSHConfigPath != "" {
		args = append(args, "--ssh-config", opts.SSHConfigPath)
	}
	args = append(args, "ui", "serve")
	return app.StartDetached(executable, args, []string{"SSH_FORWARD_CONFIG_DIR=" + opts.Layout.Dir}, opts.Layout.Dir, opts.Layout.UILog)
}

func openBrowser(url string) error {
	if os.Getenv("SSH_FORWARD_UI_NO_OPEN") == "1" {
		return nil
	}
	for _, name := range []string{"open", "xdg-open"} {
		if _, err := exec.LookPath(name); err == nil {
			return exec.Command(name, url).Start()
		}
	}
	return nil
}
