package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// ErrUsage marks a flag or host-resolution failure that should exit 2.
var ErrUsage = errors.New("usage")

// App is the CLI surface. Tests inject Manager directly and skip app.Connect.
type App struct {
	Manager core.Manager
	Options app.Options
	Version string

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
	if sshConfig, _ := cmd.Flags().GetString("ssh-config"); sshConfig != "" {
		a.Options.SSHConfigPath = sshConfig
	}
}

func jsonFlag(cmd *cobra.Command) bool {
	value, _ := cmd.Flags().GetBool("json")
	return value
}

func withInteractive(opts app.Options) app.Options {
	if app.IsTerminal(opts.Stdin) {
		opts.Interactive = true
	}
	return opts
}

func (a *App) serveManager(ctx context.Context) error {
	err := app.Serve(ctx, a.Options)
	if app.IsResolution(err) {
		return UsageError(err)
	}
	return err
}

const skipManagerKey = "skip-manager"

func annotateSkipManager(command *cobra.Command) *cobra.Command {
	if command.Annotations == nil {
		command.Annotations = map[string]string{}
	}
	command.Annotations[skipManagerKey] = "1"
	return command
}

func needsManager(cmd *cobra.Command) bool {
	if cmd.Parent() == nil {
		return false
	}
	for current := cmd; current != nil; current = current.Parent() {
		if current.Annotations[skipManagerKey] == "1" {
			return false
		}
	}
	return true
}

func (a *App) prepareCommand(cmd *cobra.Command) error {
	a.bindGlobalFlags(cmd)
	a.Options = a.Options.WithDefaults()
	a.Options = withInteractive(a.Options)
	if !needsManager(cmd) {
		return nil
	}
	return a.ensureSession(cmd.Context())
}

func (a *App) ensureSession(ctx context.Context) error {
	if a.Manager != nil {
		return nil
	}
	manager, err := app.Connect(ctx, a.Options)
	if err != nil {
		if app.IsResolution(err) {
			return UsageError(err)
		}
		return err
	}
	a.Manager = manager
	a.sessionOwned = true
	return nil
}

func flagError(cmd *cobra.Command, err error) error {
	_ = cmd
	return UsageError(err)
}

const primerText = `ssh-forward — expose Development Host ports on localhost

Daily
  status          what is forwarded right now
  add PORT        remember a remote port
  remove PORT     forget a remembered port

Host
  host            aliases from ~/.ssh/config
  default ALIAS   pin the default host

Use status --watch for live updates.

ssh-forward COMMAND --help for details.
`

func missingCommand(cmd *cobra.Command) error {
	_, err := io.WriteString(cmd.OutOrStdout(), primerText)
	return err
}

func requirePort(command, text string) (uint16, error) {
	port, err := strconv.ParseUint(text, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s requires one remote port 1..65535", command)
	}
	return uint16(port), nil
}
