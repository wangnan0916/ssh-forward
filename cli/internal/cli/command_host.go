package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/alecthomas/kong"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) runHostList(jsonOutput bool) error {
	targets, ignored, err := app.HostList(a.Options.ConfigPath)
	if err != nil {
		return err
	}
	if jsonOutput {
		return a.writeJSON(map[string]any{"hosts": targets, "ignored_hosts": ignored})
	}
	names := slices.Sorted(maps.Keys(targets))
	for _, name := range names {
		target := targets[name]
		state := "enabled"
		if target.Diagnostic != "" {
			state = "needs connection settings"
		}
		if slices.Contains(ignored, name) || slices.Contains(ignored, target.Target) {
			state = "ignored"
		}
		fmt.Fprintf(a.Options.Stdout, "%s -> %s (%s)\n", name, target.Target, state)
	}
	if len(names) == 0 {
		fmt.Fprintln(a.Options.Stdout, "No remembered hosts. Use host add TARGET or host discover.")
	}
	return nil
}

func (c *hostAddCommand) Run(a *App, grammar *commands) error {
	target := app.HostTarget{Target: c.Target}
	for _, option := range [][2]string{{"-l", c.User}, {"-i", c.Identity}, {"-J", c.Jump}, {"-F", grammar.SSHConfig}} {
		if option[1] != "" {
			target.Arguments = append(target.Arguments, option[:]...)
		}
	}
	if c.Port != 0 {
		target.Arguments = append(target.Arguments, "-p", strconv.Itoa(c.Port))
	}
	if err := app.EditHost(a.Options.ConfigPath, c.Name, &target, false); err != nil {
		return err
	}
	_, err := fmt.Fprintf(a.Options.Stdout, "Remembered host %s. Run status to start monitoring.\n", c.Name)
	return err
}

func (c *hostToggleCommand) Run(a *App, parsed *kong.Context) error {
	action := parsed.Selected().Name
	err := app.EditHost(a.Options.ConfigPath, c.Name, nil, action == "ignore")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Options.Stdout, "Host %s: %s (running manager reloads within 5 seconds).\n", c.Name, action)
	return err
}

func (*hostListCommand) Run(a *App, parent *hostCommands) error { return a.runHostList(parent.JSON) }
func (*hostDiscoverCommand) Run(a *App, ctx context.Context, parent *hostCommands) error {
	if err := app.DiscoverHosts(ctx, a.Options.ConfigPath); err != nil {
		return err
	}
	return a.runHostList(parent.JSON)
}
func (*hostAliasesCommand) Run(a *App, parent *hostCommands) error {
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
	if err != nil {
		return err
	}
	if parent.JSON {
		return a.writeJSON(hosts)
	}
	for _, host := range hosts {
		fmt.Fprintln(a.Options.Stdout, host)
	}
	return nil
}
