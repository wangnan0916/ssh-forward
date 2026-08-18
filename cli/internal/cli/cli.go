package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// ErrNeedCommand is returned when the user ran the root with no
// subcommand: the usage text has already been written to stderr.
var ErrNeedCommand = errors.New("missing command")

// ErrUsage marks a flag or host-resolution failure that should exit 2.
var ErrUsage = errors.New("usage")

// App is the CLI surface: it parses one command line and talks to the
// per-user Manager through app.Connect / app.Serve. Tests inject Manager
// directly and skip Connect.
type App struct {
	Manager core.Manager
	Host    core.HostAlias
	Options app.Options
	// PolicyReader is the shared policies-file path: Remembered Auto-forward
	// writes, policy list, and the Manager's reconciliation source share one
	// last-valid set. bindDefaults creates one when nil so tests can still
	// inject a primed reader.
	PolicyReader *app.FilePolicyReader
	Version      string

	sessionOwned bool
}

// UsageError wraps err so the process exits 2 without changing the
// message the user sees.
func UsageError(err error) error {
	if err == nil {
		return nil
	}
	return &usageError{inner: err}
}

type usageError struct{ inner error }

func (e *usageError) Error() string { return e.inner.Error() }
func (e *usageError) Unwrap() error { return e.inner }
func (e *usageError) Is(target error) bool {
	return target == ErrUsage || errors.Is(e.inner, target)
}

// Run parses and executes one command line, e.g. ["status", "--json"].
// Commands follow the product domain (cli-and-state.md); every resource
// command supports --json structured output.
func (a *App) Run(ctx context.Context, args []string) error {
	command := a.RootCommand()
	command.SetArgs(args)
	if a.Options.Stdout != nil {
		command.SetOut(a.Options.Stdout)
	} else {
		command.SetOut(io.Discard)
	}
	if a.Options.Stderr != nil {
		command.SetErr(a.Options.Stderr)
	} else {
		command.SetErr(io.Discard)
	}
	err := command.ExecuteContext(ctx)
	a.closeSession()
	return err
}

func (a *App) closeSession() {
	if !a.sessionOwned || a.Manager == nil {
		return
	}
	_ = a.Manager.Close(context.Background())
	a.sessionOwned = false
}

func (a *App) bindGlobalFlags(cmd *cobra.Command) {
	a.Options.HostFlag, _ = cmd.Flags().GetString("host")
	if policies, _ := cmd.Flags().GetString("policies"); policies != "" {
		a.Options.PoliciesPath = policies
	}
	if sshConfig, _ := cmd.Flags().GetString("ssh-config"); sshConfig != "" {
		a.Options.SSHConfigPath = sshConfig
	}
}

func (a *App) bindDefaults() {
	a.Options = a.Options.WithDefaults()
	if a.PolicyReader == nil && a.Options.PoliciesPath != "" {
		a.PolicyReader = app.NewFilePolicyReader(a.Options.PoliciesPath)
	}
}

func (a *App) connectOptions() app.Options {
	opts := a.Options
	opts.Interactive = app.IsTerminal(a.Options.Stdin)
	return opts
}

func needsManager(cmd *cobra.Command) bool {
	if cmd.Parent() == nil {
		return false
	}
	for current := cmd; current != nil; current = current.Parent() {
		switch current.Name() {
		case "host", "default", "manager", "help", "add", "remove", "policy":
			return false
		}
	}
	return true
}

func (a *App) prepareCommand(cmd *cobra.Command) error {
	a.bindGlobalFlags(cmd)
	a.bindDefaults()
	if a.Manager != nil || !needsManager(cmd) {
		return nil
	}
	session, err := app.Connect(cmd.Context(), a.connectOptions())
	if err != nil {
		if app.IsResolution(err) {
			return UsageError(err)
		}
		return err
	}
	a.Manager = session.Manager
	a.Host = session.Host
	a.PolicyReader = session.PolicyReader
	a.sessionOwned = true
	return nil
}

func flagError(cmd *cobra.Command, err error) error {
	_ = cmd
	return UsageError(err)
}

func missingCommand(cmd *cobra.Command) error {
	cmd.SetOut(cmd.ErrOrStderr())
	_ = cmd.Usage()
	return ErrNeedCommand
}

func requirePort(command, text string) (uint16, error) {
	port, ok := parsePort(text)
	if !ok {
		return 0, fmt.Errorf("%s requires one remote port 1..65535", command)
	}
	return port, nil
}
