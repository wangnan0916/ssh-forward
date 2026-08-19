package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/ui"
)

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
	url, err := app.LiveUIURL(a.Options.Layout)
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
	if err := app.StopUI(a.Options.Layout); err != nil {
		return err
	}
	fmt.Fprintln(a.Options.Stdout, "stopped")
	return nil
}

func (a *App) runUIStart(ctx context.Context) error {
	url, err := app.StartUI(ctx, a.Options)
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
	defer app.RemoveUIFiles(a.Options.Layout)
	if err := os.WriteFile(a.Options.Layout.UIPID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(a.Options.Layout.UIURL, []byte(url+"\n"), 0o600); err != nil {
		return err
	}
	server := &ui.Server{Manager: a.Manager, Ports: a.PolicyReader, Token: token}
	defer server.Close()
	return ui.Serve(ctx, listener, server.Handler())
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
