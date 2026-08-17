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

// App is the CLI surface's testable core: it owns the Manager reference
// and the output streams, and its Run method parses one command line.
// The main package is a thin shell that fills default paths, builds the
// real Manager, and delegates here.
type App struct {
	Manager      core.Manager
	Host         core.HostAlias
	HostFlag     string
	PoliciesPath string
	// PolicyReader is the shared policies-file reader (the same instance
	// the Manager was composed with): policy list reads through it so the
	// CLI and the Manager agree on the last valid set. When nil, policy
	// list parses the file directly for tests and bare usage.
	PolicyReader  *app.FilePolicyReader
	SSHConfigPath string
	ConfigPath    string
	Version       string
	Stdout        io.Writer
	Stderr        io.Writer
	// Bind fills default paths after flags parse. Main uses it so the
	// command tree does not need to know the product config directory.
	Bind func()
	// Assemble wires the Manager for commands that need one. Tests leave
	// it nil and inject Manager directly.
	Assemble func() error
	// ServeManager runs the singleton in the foreground. Main provides
	// it; tests do not.
	ServeManager func(ctx context.Context) error
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
	if a.Stdout != nil {
		command.SetOut(a.Stdout)
	} else {
		command.SetOut(io.Discard)
	}
	if a.Stderr != nil {
		command.SetErr(a.Stderr)
	} else {
		command.SetErr(io.Discard)
	}
	return command.ExecuteContext(ctx)
}

func (a *App) bindGlobalFlags(cmd *cobra.Command) {
	a.HostFlag, _ = cmd.Flags().GetString("host")
	if policies, _ := cmd.Flags().GetString("policies"); policies != "" {
		a.PoliciesPath = policies
	}
	if sshConfig, _ := cmd.Flags().GetString("ssh-config"); sshConfig != "" {
		a.SSHConfigPath = sshConfig
	}
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
	if a.Bind != nil {
		a.Bind()
	}
	if a.Manager != nil || a.Assemble == nil || !needsManager(cmd) {
		return nil
	}
	return a.Assemble()
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
