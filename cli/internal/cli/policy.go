package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *App) runPolicyList(jsonOutput bool) error {
	if a.Options.PoliciesPath == "" {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	policies, reliable, err := a.PolicyReader.Effective()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if reliable {
			fmt.Fprintf(a.Options.Stderr, "warning: %v; showing the last valid policies\n", err)
		} else {
			fmt.Fprintf(a.Options.Stderr, "warning: %v; this process has no last-valid policies\n", err)
		}
	}
	if jsonOutput {
		encoded, err := app.MarshalPolicies(policies)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Options.Stdout, string(encoded))
		return nil
	}
	return a.writePolicyListHuman(policies)
}

func (a *App) writePolicyListHuman(policies []core.ForwardingPolicy) error {
	if len(policies) == 0 {
		fmt.Fprintln(a.Options.Stdout, "Nothing remembered yet. ssh-forward add PORT")
		return nil
	}
	var remembered []string
	var other []core.ForwardingPolicy
	for _, policy := range policies {
		if port, dir, ok := core.DescribeSimpleAutoForward(policy); ok {
			if dir != "" {
				remembered = append(remembered, "  "+dir)
			} else {
				remembered = append(remembered, fmt.Sprintf("  %d", port))
			}
			continue
		}
		other = append(other, policy)
	}
	if len(remembered) != 0 {
		fmt.Fprintln(a.Options.Stdout, "Remembered:")
		for _, line := range remembered {
			fmt.Fprintln(a.Options.Stdout, line)
		}
	}
	if len(other) != 0 {
		if len(remembered) != 0 {
			fmt.Fprintln(a.Options.Stdout)
		}
		fmt.Fprintln(a.Options.Stdout, "Other policies:")
		for _, policy := range other {
			action := strings.ReplaceAll(string(policy.Action), "_", "-")
			fmt.Fprintf(a.Options.Stdout, "  %s  %s\n", policy.ID, action)
		}
	}
	return nil
}
