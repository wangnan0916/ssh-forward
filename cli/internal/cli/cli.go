package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/alecthomas/kong"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// ErrUsage marks a flag or host-resolution failure that should exit 2.
var ErrUsage = errors.New("usage")

// App is the CLI surface. Tests inject Manager directly and skip app.Connect.
type App struct {
	Manager app.Session
	Options app.Options

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

func (e *usageError) Error() string        { return e.inner.Error() }
func (e *usageError) Unwrap() error        { return e.inner }
func (e *usageError) Is(target error) bool { return target == ErrUsage }

// Run parses typed commands before opening a service session. Read-only and
// local commands never connect to or install the Manager.
func (a *App) Run(ctx context.Context, args []string) error {
	a.Options = a.Options.WithDefaults()
	grammar := commands{}
	exited := false
	parser, err := kong.New(&grammar, kong.Name("ssh-forward"),
		kong.Description("Import remote ports and publish local services through OpenSSH."),
		kong.Writers(a.Options.Stdout, a.Options.Stderr), kong.Exit(func(int) { exited = true }),
		kong.Vars{"version": "ssh-forward " + a.Options.Version}, kong.BindTo(ctx, (*context.Context)(nil)))
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	parsed, err := parser.Parse(args)
	if exited {
		return nil
	}
	if err != nil {
		return UsageError(err)
	}
	if grammar.Host != "" && !core.ValidHostName(grammar.Host) {
		return UsageError(errors.New("invalid host name"))
	}
	a.Options.HostFlag = grammar.Host
	if grammar.SSHConfig != "" {
		a.Options.SSHConfigPath = grammar.SSHConfig
	}
	a.Options.Interactive = a.Options.Interactive || app.IsTerminal(a.Options.Stdin)
	defer a.closeSession()
	return parsed.Run(a)
}

func (a *App) closeSession() {
	if a.sessionOwned && a.Manager != nil {
		_ = a.Manager.Close(context.Background())
		a.Manager = nil
		a.sessionOwned = false
	}
}

func (a *App) ensureSession(ctx context.Context) error {
	if a.Manager != nil {
		return nil
	}
	manager, err := app.Connect(ctx, a.Options)
	if err != nil {
		return err
	}
	a.Manager = manager
	a.sessionOwned = true
	return nil
}

func requirePort(command, kind, text string) (uint16, error) {
	port, err := strconv.ParseUint(text, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s requires one %s port 1..65535", command, kind)
	}
	return uint16(port), nil
}
