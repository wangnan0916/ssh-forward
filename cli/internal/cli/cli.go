package cli

import (
	"context"
	"fmt"
	"io"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/core"
)

// App is the CLI surface's testable core: it owns the Manager reference
// and the output streams, and its Run method parses one command line.
// The main package is a thin shell that builds the real Manager and
// delegates here.
type App struct {
	Manager      core.Manager
	Host         core.HostAlias
	PoliciesPath string
	// PolicyReader is the shared policies-file reader (the same instance
	// the Manager was composed with): policy list reads through it so the
	// CLI and the Manager agree on the last valid set. When nil, policy
	// list parses the file directly for tests and bare usage.
	PolicyReader *app.FilePolicyReader
	Stdout       io.Writer
	Stderr       io.Writer
}

// Run parses and executes one command line, e.g. ["status", "--json"].
// Commands follow the product domain (cli-and-state.md); every resource
// command supports --json structured output.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command (add, remove, approve, suppress, status, watch, policy)")
	}
	command, rest := args[0], args[1:]
	switch command {
	case "status":
		return a.runStatus(ctx, rest)
	case "watch":
		return a.runWatch(ctx, rest)
	case "add":
		return a.runForwardAdd(ctx, rest)
	case "remove":
		return a.runForwardRemove(ctx, rest)
	case "approve":
		return a.runListenerApprove(ctx, rest)
	case "suppress":
		return a.runListenerSuppress(ctx, rest)
	case "policy":
		return a.runPolicy(ctx, rest)
	default:
		return fmt.Errorf("unknown command %q (add, remove, approve, suppress, status, watch, policy)", command)
	}
}
