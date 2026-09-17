package cli

import "github.com/spf13/cobra"

const (
	groupDaily = "daily"
	groupHost  = "host"
)

func grouped(id string, command *cobra.Command) *cobra.Command {
	command.GroupID = id
	return command
}

func (a *App) RootCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "ssh-forward", Short: "forward ports through OpenSSH",
		Long: `ssh-forward monitors configured SSH hosts concurrently, keeps remembered forwards available on all local IPv4 interfaces, and publishes explicit local services to each host.

Rules apply to all remembered and discovered hosts by default.
Use --host TARGET for host-specific rules, status filtering, and publishing.
Use host add TARGET to remember an alias, hostname, IP, or user@host.`,
		SilenceUsage: true, SilenceErrors: true, DisableAutoGenTag: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return a.prepareCommand(cmd) },
		RunE:              func(cmd *cobra.Command, _ []string) error { return missingCommand(cmd) },
	}
	root.SetFlagErrorFunc(flagError)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddGroup(
		&cobra.Group{ID: groupDaily, Title: "Daily:"},
		&cobra.Group{ID: groupHost, Title: "Host:"},
	)
	root.PersistentFlags().String("host", "", "SSH target (alias, hostname, IP, or user@host; omit for global rules or all-host status)")
	root.PersistentFlags().String("ssh-config", "", "SSH client config file (default: ~/.ssh/config)")
	if a.Version != "" {
		root.Version = a.Version
		root.SetVersionTemplate("ssh-forward {{.Version}}\n")
		root.PersistentFlags().Bool("version", false, "print the version and exit")
	}
	root.AddCommand(
		a.rememberCommand(true),
		a.rememberCommand(false),
		a.publishCommand(true),
		a.publishCommand(false),
		a.statusCommand(),
		a.hostCommand(),
		a.doctorCommand(),
		a.uninstallCommand(),
		a.managerCommand(),
	)
	root.InitDefaultHelpCmd()
	for _, command := range root.Commands() {
		if command.Name() == "help" {
			annotateSkipManager(command)
		}
	}
	return root
}
