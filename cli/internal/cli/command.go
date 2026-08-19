package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

// RootCommand builds the cobra command tree: the command definitions and
// help text live here. Connect and Serve live in app so WebUI and CLI share
// them. The global flags — --host, --policies, --ssh-config, --version —
// are persistent flags so they precede any subcommand and appear in help.
func (a *App) RootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ssh-forward",
		Short: "expose Development Host ports locally through system OpenSSH",
		Long: "ssh-forward exposes eligible ports on a Linux Development Host " +
			"through your system OpenSSH connection, preferring the same port locally.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.prepareCommand(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return missingCommand(cmd)
		},
	}
	root.SetFlagErrorFunc(flagError)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().String("host", "", "Development Host alias (default: config.jsonc's default_host)")
	root.PersistentFlags().String("policies", "", "path to policies.jsonc (default: the product config directory)")
	root.PersistentFlags().String("ssh-config", "", "SSH client config file (default: the user's ~/.ssh/config)")
	if a.Version != "" {
		root.Version = a.Version
		root.SetVersionTemplate("ssh-forward {{.Version}}\n")
		root.PersistentFlags().Bool("version", false, "print the version and exit")
	}

	root.AddCommand(
		a.addCommand(),
		a.removeCommand(),
		a.statusCommand(),
		a.watchCommand(),
		a.policyCommand(),
		a.hostCommand(),
		a.defaultCommand(),
		a.managerCommand(),
	)
	root.InitDefaultHelpCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "help" {
			annotateSkipManager(cmd)
			break
		}
	}
	return root
}

func (a *App) addCommand() *cobra.Command {
	var dir string
	command := &cobra.Command{
		Use:   "add [port]",
		Short: "remember a remote port or project directory to auto-forward",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runRemember(cmd, args, dir, true)
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "Development Host working directory to auto-forward")
	command.Flags().Bool("json", false, "emit JSON")
	return annotateSkipManager(command)
}

func (a *App) removeCommand() *cobra.Command {
	var dir string
	command := &cobra.Command{
		Use:   "remove [port]",
		Short: "forget a remembered port or project directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runRemember(cmd, args, dir, false)
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "Development Host working directory to forget")
	command.Flags().Bool("json", false, "emit JSON")
	return annotateSkipManager(command)
}

func (a *App) runRemember(cmd *cobra.Command, args []string, dir string, adding bool) error {
	if a.Options.PoliciesPath == "" {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	if dir != "" && len(args) != 0 {
		return UsageError(fmt.Errorf("usage: ssh-forward %s PORT  or  ssh-forward %s --dir PATH", cmd.Name(), cmd.Name()))
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if dir != "" {
		return a.rememberDir(dir, adding, jsonOutput)
	}
	if len(args) != 1 {
		return UsageError(fmt.Errorf("usage: ssh-forward %s PORT  or  ssh-forward %s --dir PATH", cmd.Name(), cmd.Name()))
	}
	port, err := requirePort(cmd.Name(), args[0])
	if err != nil {
		return UsageError(err)
	}
	return a.rememberPort(port, adding, jsonOutput)
}

func (a *App) rememberPort(port uint16, adding, jsonOutput bool) error {
	var (
		changed bool
		err     error
	)
	if adding {
		changed, err = a.PolicyReader.AddPort(port)
	} else {
		changed, err = a.PolicyReader.RemovePort(port)
	}
	if err != nil {
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("port %d is not remembered", port)
	}
	return a.writeRemember(jsonOutput, adding, changed, port, "")
}

func (a *App) rememberDir(dir string, adding, jsonOutput bool) error {
	var (
		changed bool
		stored  string
		err     error
	)
	if adding {
		changed, stored, err = a.PolicyReader.AddDir(dir)
	} else {
		changed, stored, err = a.PolicyReader.RemoveDir(dir)
	}
	if err != nil {
		if errorsIsHostDir(err) {
			return UsageError(err)
		}
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("directory %s is not remembered", stored)
	}
	return a.writeRemember(jsonOutput, adding, changed, 0, stored)
}

func errorsIsHostDir(err error) bool {
	return errors.Is(err, app.ErrEmptyDirectory) || errors.Is(err, app.ErrHostDirectory)
}

func (a *App) statusCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "status",
		Short: "show the host and the active forwards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput, _ := cmd.Flags().GetBool("json")
			snapshot, err := a.Manager.Snapshot(cmd.Context())
			if err != nil {
				return err
			}
			if snapshot.Host == nil {
				return fmt.Errorf("no Development Host is configured")
			}
			if jsonOutput {
				return a.writeSnapshotJSON(snapshot)
			}
			return a.writeStatusHuman(snapshot)
		},
	}
	command.Flags().Bool("json", false, "emit the wire-shaped snapshot")
	return command
}

func (a *App) watchCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "watch",
		Short: "stream live state until interrupted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput, _ := cmd.Flags().GetBool("json")
			return a.runWatch(cmd.Context(), jsonOutput)
		},
	}
	command.Flags().Bool("json", false, "emit one wire-shaped snapshot per line")
	return command
}

func (a *App) policyCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "policy",
		Short: "show forwarding policies",
		RunE: func(cmd *cobra.Command, args []string) error {
			return UsageError(fmt.Errorf("policy needs a subcommand (list)"))
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "show the forwarding policies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput, _ := cmd.Flags().GetBool("json")
			return a.runPolicyList(jsonOutput)
		},
	}
	list.Flags().Bool("json", false, "emit the policies in the file shape")
	command.AddCommand(list)
	return annotateSkipManager(command)
}

func (a *App) hostCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "host",
		Short: "host discovery from the SSH client configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return UsageError(fmt.Errorf("host needs a subcommand (list)"))
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "show hosts from the SSH client configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput, _ := cmd.Flags().GetBool("json")
			return a.runHostList(jsonOutput)
		},
	}
	list.Flags().Bool("json", false, "emit the host list as JSON")
	command.AddCommand(list)
	return annotateSkipManager(command)
}

func (a *App) defaultCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "default <alias>",
		Short: "pin the default Development Host",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return UsageError(fmt.Errorf("usage: ssh-forward default ALIAS"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSetDefault(args[0])
		},
	}
	return annotateSkipManager(command)
}

func (a *App) managerCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "manager",
		Short: "run the singleton manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			return UsageError(fmt.Errorf("manager needs a subcommand (serve)"))
		},
	}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "run the singleton manager in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.serveManager(cmd.Context())
		},
	}
	command.AddCommand(serve)
	return annotateSkipManager(command)
}
