package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
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

// listenerID is the Remote Listener identity on the wire: family, bind
// scope, and port. Status rendering compares identities, not fields — the
// identity triple has one construction and one equality here.
type listenerID struct {
	family core.AddressFamily
	scope  core.ListenerBindScope
	port   uint16
}

func idOfListener(listener core.ListenerObservation) listenerID {
	return listenerID{family: listener.Family, scope: listener.BindScope, port: listener.RemotePort}
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

	// Forwards first: the things this user's own commands created are
	// what status is for.
	if len(host.Forwards) != 0 {
		builder.WriteString("Forwards:\n")
		for _, forward := range host.Forwards {
			fmt.Fprintf(&builder, "  %s (%s) → %s:%d (local %d)\n",
				forward.ID, forward.Kind, forward.RemoteFamily, forward.RemotePort, forward.AllocatedLocalPort)
		}
	}

	// Listeners stay out of the human view: the user asked for the active
	// forwards, not a port dump. Only listeners needing a decision get a
	// one-line heads-up — that is the Ask flow's only surface.
	if len(host.AskListeners) != 0 {
		ports := make([]string, 0, len(host.AskListeners))
		for _, candidate := range host.AskListeners {
			ports = append(ports, fmt.Sprintf("%d", candidate.RemotePort))
		}
		fmt.Fprintf(&builder, "Listeners needing a decision: %s (approve or suppress them)\n", strings.Join(ports, ", "))
	}
	_, err := io.WriteString(a.Stdout, builder.String())
	return err
}

// runForwardAdd is the top-level "add" command: forward one remote port.
func (a *App) runForwardAdd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := newCommandFlags(flags)
	positional, err := parseResourceFlags(flags, args)
	if err != nil {
		return err
	}
	port, err := positionalPort(positional, "add")
	if err != nil {
		return err
	}
	if existing, found := a.existingManualForward(ctx, port); found {
		fmt.Fprintf(a.Stdout, "port %d already forwarded (local %d)\n", port, existing.AllocatedLocalPort)
		return nil
	}
	family, err := common.family()
	if err != nil {
		return err
	}
	outcome, err := a.Manager.Execute(ctx, core.AddManualForward{
		CommandID:  common.operationIDOrRandom(),
		Host:       a.Host,
		RemotePort: port,
		Family:     family,
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *common.jsonOutput)
}

// runForwardRemove is the top-level "remove" command. The argument is the
// port you added ("remove 8000" — the natural counterpart of add), or an
// explicit forward ID from status for scripts and managed forwards.
func (a *App) runForwardRemove(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit the wire-shaped outcome")
	operationID := flags.String("operation-id", "", "stable operation ID for retries")
	positional, err := parseResourceFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("remove requires one port or forward ID")
	}
	target := positional[0]
	if port, ok := parsePort(target); ok {
		return a.removeByPort(ctx, port, *jsonOutput)
	}
	outcome, err := a.Manager.Execute(ctx, core.RemoveForward{
		CommandID: core.CommandID(operationIDOrRandom(*operationID)),
		ForwardID: core.ForwardID(target),
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *jsonOutput)
}

// existingManualForward reports an active Manual Forward on the remote
// port: add is idempotent, so repeating "add 5173" never creates a second
// forward (the local port would silently shift — surprising).
func (a *App) existingManualForward(ctx context.Context, port uint16) (core.ForwardSnapshot, bool) {
	snapshot, err := a.Manager.Snapshot(ctx)
	if err != nil || snapshot.Host == nil {
		return core.ForwardSnapshot{}, false
	}
	for _, forward := range snapshot.Host.Forwards {
		if forward.RemotePort == port && forward.Kind == core.ForwardManual {
			return forward, true
		}
	}
	return core.ForwardSnapshot{}, false
}

// parsePort reports whether the argument is a plain port number.
func parsePort(text string) (uint16, bool) {
	port, err := strconv.ParseUint(text, 10, 16)
	if err != nil || port == 0 {
		return 0, false
	}
	return uint16(port), true
}

// removeByPort tears down every Manual Forward on the remote port — the
// forwards this user's own add commands created. A port served only by a
// Managed Forward names the policy (or the ID) instead, so reconciliation
// cannot be fought by accident.
func (a *App) removeByPort(ctx context.Context, port uint16, jsonOutput bool) error {
	snapshot, err := a.Manager.Snapshot(ctx)
	if err != nil {
		return err
	}
	var manualIDs []core.ForwardID
	var managedID string
	if snapshot.Host != nil {
		for _, forward := range snapshot.Host.Forwards {
			if forward.RemotePort != port {
				continue
			}
			if forward.Kind == core.ForwardManual {
				manualIDs = append(manualIDs, forward.ID)
			} else if managedID == "" {
				managedID = string(forward.ID)
			}
		}
	}
	if len(manualIDs) == 0 {
		if managedID != "" {
			return fmt.Errorf("port %d is served by the managed forward %s; remove it by ID or change the policy", port, managedID)
		}
		return fmt.Errorf("no forward on port %d", port)
	}
	for _, id := range manualIDs {
		outcome, err := a.Manager.Execute(ctx, core.RemoveForward{
			CommandID: core.CommandID(operationIDOrRandom("")),
			ForwardID: id,
		})
		if err != nil {
			return err
		}
		if err := a.writeOutcome(outcome, jsonOutput); err != nil {
			return err
		}
	}
	return nil
}

// runListenerApprove is the top-level "approve" command: a One-time
// Approval for the Listener on the given remote port.
func (a *App) runListenerApprove(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("approve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := newCommandFlags(flags)
	positional, err := parseResourceFlags(flags, args)
	if err != nil {
		return err
	}
	port, err := positionalPort(positional, "approve")
	if err != nil {
		return err
	}
	family, err := common.family()
	if err != nil {
		return err
	}
	outcome, err := a.Manager.Execute(ctx, core.ApproveListener{
		CommandID:  common.operationIDOrRandom(),
		Host:       a.Host,
		RemotePort: port,
		Family:     family,
	})
	if err != nil {
		return err
	}
	return a.writeOutcome(outcome, *common.jsonOutput)
}

// runListenerSuppress is the top-level "suppress" command: a One-time
// Suppression for the Listener on the given remote port.
func (a *App) runListenerSuppress(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("suppress", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := newCommandFlags(flags)
	positional, err := parseResourceFlags(flags, args)
	if err != nil {
		return err
	}
	port, err := positionalPort(positional, "suppress")
	if err != nil {
		return err
	}
	family, err := common.family()
	if err != nil {
		return err
	}
	outcome, err := a.Manager.Execute(ctx, core.SuppressListener{
		CommandID:  common.operationIDOrRandom(),
		Host:       a.Host,
		RemotePort: port,
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
