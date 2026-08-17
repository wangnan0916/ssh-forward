package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ssh-forward/cli/internal/core"
)

// RootCommand builds the cobra command tree: the command definitions and
// help text live here (main assembles the Manager and executes). The
// global flags — --host, --policies, --ssh-config, --version — are
// persistent flags so they precede any subcommand and appear in help.
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
		a.approveCommand(),
		a.suppressCommand(),
		a.statusCommand(),
		a.watchCommand(),
		a.policyCommand(),
		a.hostCommand(),
		a.defaultCommand(),
		a.managerCommand(),
	)
	return root
}

func (a *App) addCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "add <port>",
		Short: "forward one remote port to the local machine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			port, err := requirePort("add", args[0])
			if err != nil {
				return err
			}
			if existing, found := a.existingManualForward(ctx, port); found {
				fmt.Fprintf(a.Stdout, "port %d already forwarded (local %d)\n", port, existing.AllocatedLocalPort)
				return nil
			}
			family, err := familyFlag(cmd)
			if err != nil {
				return err
			}
			jsonOutput, _ := cmd.Flags().GetBool("json")
			operationID, _ := cmd.Flags().GetString("operation-id")
			outcome, err := a.Manager.Execute(ctx, core.AddManualForward{
				CommandID:  core.CommandID(operationIDOrRandom(operationID)),
				Host:       a.Host,
				RemotePort: port,
				Family:     family,
			})
			if err != nil {
				return err
			}
			return a.writeOutcome(outcome, jsonOutput)
		},
	}
	command.Flags().Bool("json", false, "emit the wire-shaped outcome")
	command.Flags().String("family", "auto", "auto, ipv4, or ipv6")
	command.Flags().String("operation-id", "", "stable operation ID for retries")
	return command
}

func (a *App) removeCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "remove <port|forward-id>",
		Short: "tear down a forward (by port, or by ID from status)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			jsonOutput, _ := cmd.Flags().GetBool("json")
			operationID, _ := cmd.Flags().GetString("operation-id")
			if port, ok := parsePort(args[0]); ok {
				return a.removeByPort(ctx, port, jsonOutput)
			}
			outcome, err := a.Manager.Execute(ctx, core.RemoveForward{
				CommandID: core.CommandID(operationIDOrRandom(operationID)),
				ForwardID: core.ForwardID(args[0]),
			})
			if err != nil {
				return err
			}
			return a.writeOutcome(outcome, jsonOutput)
		},
	}
	command.Flags().Bool("json", false, "emit the wire-shaped outcome")
	command.Flags().String("operation-id", "", "stable operation ID for retries")
	return command
}

func (a *App) approveCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "approve <port>",
		Short: "allow a listener in the Ask flow (One-time Approval)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			port, err := requirePort("approve", args[0])
			if err != nil {
				return err
			}
			family, err := familyFlag(cmd)
			if err != nil {
				return err
			}
			jsonOutput, _ := cmd.Flags().GetBool("json")
			operationID, _ := cmd.Flags().GetString("operation-id")
			outcome, err := a.Manager.Execute(ctx, core.ApproveListener{
				CommandID:  core.CommandID(operationIDOrRandom(operationID)),
				Host:       a.Host,
				RemotePort: port,
				Family:     family,
			})
			if err != nil {
				return err
			}
			return a.writeOutcome(outcome, jsonOutput)
		},
	}
	command.Flags().Bool("json", false, "emit the wire-shaped outcome")
	command.Flags().String("family", "auto", "auto, ipv4, or ipv6")
	command.Flags().String("operation-id", "", "stable operation ID for retries")
	return command
}

func (a *App) suppressCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "suppress <port>",
		Short: "silence a listener in the Ask flow (One-time Suppression)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			port, err := requirePort("suppress", args[0])
			if err != nil {
				return err
			}
			family, err := familyFlag(cmd)
			if err != nil {
				return err
			}
			jsonOutput, _ := cmd.Flags().GetBool("json")
			operationID, _ := cmd.Flags().GetString("operation-id")
			outcome, err := a.Manager.Execute(ctx, core.SuppressListener{
				CommandID:  core.CommandID(operationIDOrRandom(operationID)),
				Host:       a.Host,
				RemotePort: port,
				Family:     family,
			})
			if err != nil {
				return err
			}
			return a.writeOutcome(outcome, jsonOutput)
		},
	}
	command.Flags().Bool("json", false, "emit the wire-shaped outcome")
	command.Flags().String("family", "auto", "auto, ipv4, or ipv6")
	command.Flags().String("operation-id", "", "stable operation ID for retries")
	return command
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
		Short: "manage forwarding policies",
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
	return command
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
	return command
}

func (a *App) defaultCommand() *cobra.Command {
	return &cobra.Command{
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
			if a.ServeManager == nil {
				return fmt.Errorf("manager serve is only available from the ssh-forward command")
			}
			return a.ServeManager(cmd.Context())
		},
	}
	command.AddCommand(serve)
	return command
}

// familyFlag validates the --family flag exactly like the wire adapter, so
// a bad family reports invalid parameters instead of a misleading
// Listener-not-found.
func familyFlag(cmd *cobra.Command) (core.AddressFamily, error) {
	text, _ := cmd.Flags().GetString("family")
	family := core.AddressFamily(text)
	if !core.ValidAddressFamily(family) {
		return "", fmt.Errorf("invalid --family %q (auto, ipv4, or ipv6)", text)
	}
	return family, nil
}
