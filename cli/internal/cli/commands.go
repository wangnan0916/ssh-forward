package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/jsonrpc"
)

// runStatus prints the current Snapshot: --json emits the wire shape;
// the default human view summarizes the host, discovery, listeners
// (with their Lifetime and Ask status), and Forwards.
func (a *App) runStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit the wire-shaped Snapshot")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("status takes no arguments")
	}
	snapshot, err := a.Manager.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Host == nil {
		return fmt.Errorf("no Development Host is configured")
	}
	if *jsonOutput {
		encoded, err := jsonrpc.MarshalSnapshot(snapshot)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, string(encoded))
		return nil
	}
	return a.writeStatusHuman(snapshot)
}

func (a *App) writeStatusHuman(snapshot core.Snapshot) error {
	host := snapshot.Host
	var builder strings.Builder
	fmt.Fprintf(&builder, "Host: %s — %s\n", host.Alias, host.Connection)
	fmt.Fprintf(&builder, "Discovery: %s (baseline %v, scanner v%d)\n",
		host.Discovery.State, host.Discovery.BaselineEstablished, host.Discovery.ScannerVersion)
	if host.Discovery.Diagnostic != "" {
		fmt.Fprintf(&builder, "  diagnostic: %s\n", host.Discovery.Diagnostic)
	}
	if len(host.ListenerObservations) != 0 {
		builder.WriteString("Listeners:\n")
		for _, listener := range host.ListenerObservations {
			status := lifetimeStatus(host.ListenerLifetimes, listener)
			ask := ""
			for _, candidate := range host.AskListeners {
				if candidate.Family == listener.Family && candidate.BindScope == listener.BindScope && candidate.RemotePort == listener.RemotePort {
					ask = " — Ask"
				}
			}
			fmt.Fprintf(&builder, "  %d/%s %s — %s%s\n",
				listener.RemotePort, listener.Family, listener.BindScope, status, ask)
		}
	}
	if len(host.Forwards) != 0 {
		builder.WriteString("Forwards:\n")
		for _, forward := range host.Forwards {
			fmt.Fprintf(&builder, "  %s (%s) → %s:%d (local %d)\n",
				forward.ID, forward.Kind, forward.RemoteFamily, forward.RemotePort, forward.AllocatedLocalPort)
		}
	}
	_, err := io.WriteString(a.Stdout, builder.String())
	return err
}

func lifetimeStatus(lifetimes []core.ListenerLifetimeSnapshot, listener core.ListenerObservation) string {
	for _, lifetime := range lifetimes {
		if lifetime.Family == listener.Family && lifetime.BindScope == listener.BindScope && lifetime.RemotePort == listener.RemotePort {
			return string(lifetime.Status)
		}
	}
	return "untracked"
}

// runForward executes the forward command family: add and remove.
func (a *App) runForward(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("forward needs a subcommand (add, remove)")
	}
	switch args[0] {
	case "add":
		return a.runForwardAdd(ctx, args[1:])
	case "remove":
		return a.runForwardRemove(ctx, args[1:])
	default:
		return fmt.Errorf("unknown forward subcommand %q (add, remove)", args[0])
	}
}

func (a *App) runForwardAdd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("forward add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := newCommandFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("forward add takes no positional arguments")
	}
	if err := common.requireRemotePort(); err != nil {
		return err
	}
	family, err := common.family()
	if err != nil {
		return err
	}
	outcome, err := a.Manager.Execute(ctx, core.AddManualForward{
		CommandID:  common.operationIDOrRandom(),
		Host:       a.Host,
		RemotePort: uint16(*common.remotePort),
		Family:     family,
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *common.jsonOutput)
}

func (a *App) runForwardRemove(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("forward remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit the wire-shaped outcome")
	forwardID := flags.String("forward-id", "", "forward ID to remove")
	operationID := flags.String("operation-id", "", "stable operation ID for retries")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *forwardID == "" {
		return fmt.Errorf("forward remove requires --forward-id")
	}
	outcome, err := a.Manager.Execute(ctx, core.RemoveForward{
		CommandID: core.CommandID(operationIDOrRandom(*operationID)),
		ForwardID: core.ForwardID(*forwardID),
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *jsonOutput)
}

// runListener executes the listener decision family: approve and suppress.
func (a *App) runListener(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("listener needs a subcommand (approve, suppress)")
	}
	switch args[0] {
	case "approve":
		return a.runListenerApprove(ctx, args[1:])
	case "suppress":
		return a.runListenerSuppress(ctx, args[1:])
	default:
		return fmt.Errorf("unknown listener subcommand %q (approve, suppress)", args[0])
	}
}

func (a *App) runListenerApprove(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("listener approve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := newCommandFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := common.requireRemotePort(); err != nil {
		return err
	}
	family, err := common.family()
	if err != nil {
		return err
	}
	outcome, err := a.Manager.Execute(ctx, core.ApproveListener{
		CommandID:  common.operationIDOrRandom(),
		Host:       a.Host,
		RemotePort: uint16(*common.remotePort),
		Family:     family,
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *common.jsonOutput)
}

func (a *App) runListenerSuppress(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("listener suppress", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := newCommandFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := common.requireRemotePort(); err != nil {
		return err
	}
	family, err := common.family()
	if err != nil {
		return err
	}
	outcome, err := a.Manager.Execute(ctx, core.SuppressListener{
		CommandID:  common.operationIDOrRandom(),
		Host:       a.Host,
		RemotePort: uint16(*common.remotePort),
		Family:     family,
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *common.jsonOutput)
}

func (a *App) writeOutcome(outcome core.Outcome, jsonOutput bool) error {
	if jsonOutput {
		encoded, err := jsonrpc.MarshalOutcome(outcome)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, string(encoded))
		return nil
	}
	if outcome.Forward.ID != "" {
		fmt.Fprintf(a.Stdout, "%s: %s → %s:%d (local %d)\n",
			outcome.Kind, outcome.Forward.ID, outcome.Forward.RemoteFamily, outcome.Forward.RemotePort, outcome.Forward.AllocatedLocalPort)
	} else {
		fmt.Fprintf(a.Stdout, "%s\n", outcome.Kind)
	}
	return nil
}

// operationIDOrRandom supplies a unique operation ID when the caller did
// not provide a stable one: commands are deduplicated by operation ID, so
// a fresh value per invocation keeps retries distinct.
func operationIDOrRandom(provided string) string {
	if provided != "" {
		return provided
	}
	return fmt.Sprintf("cli-%d", time.Now().UnixNano())
}
