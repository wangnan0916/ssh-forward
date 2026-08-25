package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) hostCommand() *cobra.Command {
	run := func(cmd *cobra.Command, _ []string) error { return a.runHostList(jsonFlag(cmd)) }
	command := &cobra.Command{Use: "host", Short: "list Host aliases from ~/.ssh/config", Args: cobra.NoArgs, RunE: run}
	command.PersistentFlags().Bool("json", false, "emit JSON")
	command.AddCommand(&cobra.Command{Use: "list", Short: "list Host aliases", Args: cobra.NoArgs, RunE: run})
	return grouped(groupHost, annotateSkipManager(command))
}

func (a *App) defaultCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "default [alias]", Short: "show or pin the default SSH Host", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.runShowDefault()
			}
			return a.runSetDefault(args[0])
		},
	}
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
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
	if err != nil {
		return err
	}
	if jsonOutput {
		return a.writeJSON(hosts)
	}
	fmt.Fprintln(a.Options.Stdout, "Hosts in ~/.ssh/config:")
	selected, _ := app.PinnedHost(a.Options.ConfigPath)
	for _, host := range hosts {
		suffix := ""
		if host == selected {
			suffix = "  (default)"
		}
		fmt.Fprintln(a.Options.Stdout, "  "+host+suffix)
	}
	if len(hosts) == 0 {
		fmt.Fprintln(a.Options.Stdout, "No Host aliases found.")
	} else if selected == "" {
		fmt.Fprintln(a.Options.Stdout, "Pin one: ssh-forward default ALIAS")
	}
	return nil
}

func (a *App) runShowDefault() error {
	host, err := app.PinnedHost(a.Options.ConfigPath)
	if errors.Is(err, app.ErrNoHost) {
		fmt.Fprintln(a.Options.Stdout, "No default host. Pin one with: ssh-forward default ALIAS")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host: %s\n", host)
	return nil
}

func (a *App) runSetDefault(alias string) error {
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
	if err != nil {
		return err
	}
	if !slices.Contains(hosts, alias) {
		return UsageError(fmt.Errorf("%s is not a literal Host alias in your SSH config", alias))
	}
	if err := app.SetDefaultHost(a.Options.ConfigPath, alias); err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host set to %s\n", alias)
	return nil
}
