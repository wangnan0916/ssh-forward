package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/core"
)

// runPolicy executes the policy command family. The policy file is the
// single source of truth (ADR-0005); the Manager reconciles external edits
// on its own cadence, so this surface only reads and validates.
func (a *App) runPolicy(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("policy needs a subcommand (list)")
	}
	switch args[0] {
	case "list":
		return a.runPolicyList(args[1:])
	default:
		return fmt.Errorf("unknown policy subcommand %q (list)", args[0])
	}
}

func (a *App) runPolicyList(args []string) error {
	flags := flag.NewFlagSet("policy list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit policies as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if a.PoliciesPath == "" {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	var policies []core.ForwardingPolicy
	var err error
	if a.PolicyReader != nil {
		policies, err = a.PolicyReader.Read()
		if err != nil {
			fmt.Fprintf(a.Stderr, "warning: %v; showing the last valid policies\n", err)
		}
	} else {
		policies, err = app.LoadPolicies(a.PoliciesPath)
		if err != nil {
			return err
		}
	}
	if *jsonOutput {
		encoded, err := app.MarshalPolicies(policies)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, string(encoded))
		return nil
	}
	if len(policies) == 0 {
		fmt.Fprintln(a.Stdout, "no policies")
		return nil
	}
	for _, policy := range policies {
		fmt.Fprintf(a.Stdout, "%s priority=%d action=%s\n", policy.ID, policy.Priority, policy.Action)
	}
	return nil
}
