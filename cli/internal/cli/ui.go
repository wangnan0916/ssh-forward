package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) uiCommand() *cobra.Command {
	runStart := func(cmd *cobra.Command, args []string) error {
		return a.runUIStart(cmd.Context())
	}
	command := &cobra.Command{
		Use:   "ui",
		Short: "open the loopback page",
		Args:  cobra.NoArgs,
		RunE:  runStart,
	}
	start := &cobra.Command{
		Use:   "start",
		Short: "start the WebUI in the background",
		Args:  cobra.NoArgs,
		RunE:  runStart,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "print the WebUI URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUIStatus(jsonFlag(cmd))
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
	return grouped(groupDaily, annotateSkipManager(command))
}

func uiNotRunning(err error) error {
	if errors.Is(err, app.ErrUINotRunning) {
		return fmt.Errorf("WebUI is not running. Start it: ssh-forward ui")
	}
	return err
}

func (a *App) runUIStatus(jsonOutput bool) error {
	url, err := app.LiveUIURL(a.Options.Layout)
	if err != nil {
		return uiNotRunning(err)
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
	if err := app.StopUI(a.Options.Layout); err != nil {
		return uiNotRunning(err)
	}
	fmt.Fprintln(a.Options.Stdout, "Stopped the WebUI.")
	return nil
}

func (a *App) runUIStart(ctx context.Context) error {
	url, err := app.StartUI(ctx, a.connectOptions())
	if err != nil {
		if app.IsResolution(err) {
			return UsageError(err)
		}
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
	return app.ServeUISession(ctx, a.connectOptions(), app.Session{
		Manager:      a.Manager,
		Host:         a.Host,
		PolicyReader: a.PolicyReader,
	})
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
