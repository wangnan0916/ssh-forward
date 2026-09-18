package cli

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) hostCommand() *cobra.Command {
	run := func(cmd *cobra.Command, _ []string) error { return a.runHostList(jsonFlag(cmd)) }
	command := &cobra.Command{Use: "host", Short: "list remembered and discovered SSH hosts", Args: cobra.NoArgs, RunE: run}
	command.PersistentFlags().Bool("json", false, "emit JSON")
	command.AddCommand(&cobra.Command{Use: "list", Short: "list remembered and discovered hosts", Args: cobra.NoArgs, RunE: run})
	command.AddCommand(a.hostAddCommand())
	for _, action := range []string{"ignore", "enable"} {
		command.AddCommand(&cobra.Command{Use: action + " HOST", Short: action + " a monitored host", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if cmd.Name() == "enable" {
				err = app.EnableHost(a.Options.ConfigPath, args[0])
			} else {
				err = app.SetHost(a.Options.ConfigPath, args[0], app.HostTarget{}, true)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Options.Stdout, "Host %s: %s (running manager reloads within 5 seconds).\n", args[0], cmd.Name())
			return nil
		}})
	}
	command.AddCommand(&cobra.Command{Use: "discover", Short: "remember active local SSH targets", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := app.DiscoverHosts(cmd.Context(), a.Options.ConfigPath); err != nil {
			return err
		}
		return a.runHostList(jsonFlag(cmd))
	}})
	command.AddCommand(&cobra.Command{Use: "aliases", Short: "list candidates from SSH config without connecting", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
		if err != nil {
			return err
		}
		if jsonFlag(cmd) {
			return a.writeJSON(hosts)
		}
		for _, host := range hosts {
			fmt.Fprintln(a.Options.Stdout, host)
		}
		return nil
	}})
	return grouped(groupHost, annotateSkipManager(command))
}

func (a *App) uninstallCommand() *cobra.Command {
	return annotateSkipManager(&cobra.Command{
		Use: "uninstall", Short: "remove the background manager", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := app.Uninstall(a.Options.Layout); err != nil {
				return err
			}
			fmt.Fprintln(a.Options.Stdout, "Background manager removed; configuration kept.")
			return nil
		},
	})
}

func (a *App) managerCommand() *cobra.Command {
	command := annotateSkipManager(&cobra.Command{Use: "manager", Hidden: true})
	command.AddCommand(&cobra.Command{
		Use: "serve", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.serveManager(cmd.Context()) },
	})
	return command
}

func (a *App) runHostList(jsonOutput bool) error {
	targets, ignored, err := app.HostList(a.Options.ConfigPath)
	if err != nil {
		return err
	}
	if jsonOutput {
		return a.writeJSON(map[string]any{"hosts": targets, "ignored_hosts": ignored})
	}
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	slices.Sort(names)
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

func (a *App) hostAddCommand() *cobra.Command {
	command := &cobra.Command{Use: "add HOST", Short: "remember an SSH target for global rules", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target := app.HostTarget{Target: args[0]}
		if value, _ := cmd.Flags().GetString("target"); value != "" {
			target.Target = value
		}
		for _, pair := range [][2]string{{"user", "-l"}, {"identity", "-i"}, {"jump", "-J"}} {
			if value, _ := cmd.Flags().GetString(pair[0]); value != "" {
				target.Arguments = append(target.Arguments, pair[1], value)
			}
		}
		if value, _ := cmd.Flags().GetInt("port"); value != 0 {
			target.Arguments = append(target.Arguments, "-p", strconv.Itoa(value))
		}
		if cmd.Flags().Changed("ssh-config") {
			target.Arguments = append(target.Arguments, "-F", a.Options.SSHConfigPath)
		}
		if err := app.SetHost(a.Options.ConfigPath, args[0], target, false); err != nil {
			return err
		}
		fmt.Fprintf(a.Options.Stdout, "Remembered host %s. Run status to start monitoring.\n", args[0])
		return nil
	}}
	command.Flags().String("target", "", "SSH destination when different from the stored name")
	command.Flags().String("user", "", "SSH username")
	command.Flags().String("identity", "", "absolute private-key path")
	command.Flags().String("jump", "", "SSH jump host")
	command.Flags().Int("port", 0, "SSH port")
	return command
}
