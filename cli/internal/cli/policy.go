package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) runPolicyList(jsonOutput bool) error {
	if a.PoliciesPath == "" {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	policies, err := a.PolicyReader.Read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(a.Stderr, "warning: %v; showing the last valid policies\n", err)
	}
	if jsonOutput {
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
