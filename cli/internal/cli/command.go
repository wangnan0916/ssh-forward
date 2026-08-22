package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	groupDaily = "daily"
	groupHost  = "host"
	groupMore  = "more"
)

func grouped(id string, command *cobra.Command) *cobra.Command {
	command.GroupID = id
	return command
}

func (a *App) RootCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "ssh-forward", Short: "forward Development Host ports through OpenSSH",
		Long: `ssh-forward shows loopback TCP listeners on one SSH host and keeps remembered ports available on localhost.

Name the host with --host ALIAS (-h is help). Pin one with: ssh-forward default ALIAS`,
		SilenceUsage: true, SilenceErrors: true, DisableAutoGenTag: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return a.prepareCommand(cmd) },
		RunE:              func(cmd *cobra.Command, _ []string) error { return missingCommand(cmd) },
	}
	root.SetFlagErrorFunc(flagError)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddGroup(
		&cobra.Group{ID: groupDaily, Title: "Daily:"},
		&cobra.Group{ID: groupHost, Title: "Host:"},
		&cobra.Group{ID: groupMore, Title: "More:"},
	)
	root.PersistentFlags().String("host", "", "SSH Host alias (default: the pinned host)")
	root.PersistentFlags().String("ssh-config", "", "SSH client config file (default: ~/.ssh/config)")
	if a.Version != "" {
		root.Version = a.Version
		root.SetVersionTemplate("ssh-forward {{.Version}}\n")
		root.PersistentFlags().Bool("version", false, "print the version and exit")
	}
	root.AddCommand(
		a.rememberCommand(true), a.rememberCommand(false), a.statusCommand(), a.watchCommand(),
		a.hostCommand(), a.defaultCommand(), a.managerCommand(),
	)
	root.SetHelpCommandGroupID(groupMore)
	root.InitDefaultHelpCmd()
	for _, command := range root.Commands() {
		if command.Name() == "help" {
			annotateSkipManager(command)
		}
	}
	return root
}

func (a *App) rememberCommand(adding bool) *cobra.Command {
	name, verb, short := "add", "remember", "remember a remote port"
	if !adding {
		name, verb, short = "remove", "forget", "forget a remembered port"
	}
	command := &cobra.Command{
		Use: name + " PORT", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := requirePort(name, args[0])
			if err != nil {
				return UsageError(err)
			}
			return a.rememberPort(cmd.Context(), port, adding, jsonFlag(cmd))
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	command.Example = fmt.Sprintf("  ssh-forward %s 5173  # %s port 5173", name, verb)
	return grouped(groupDaily, command)
}

func (a *App) rememberPort(ctx context.Context, port uint16, adding, jsonOutput bool) error {
	status, err := a.Manager.Status(ctx)
	if err != nil {
		return err
	}
	host := string(status.Host)
	var changed bool
	if adding {
		changed, err = app.AddPort(a.Options.ConfigPath, host, port)
	} else {
		changed, err = app.RemovePort(a.Options.ConfigPath, host, port)
	}
	if err != nil {
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("port %d is not remembered for %s", port, host)
	}
	return a.writeRemember(jsonOutput, adding, changed, host, port)
}

func (a *App) statusCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "status", Short: "show listeners and forwards", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch, _ := cmd.Flags().GetBool("watch"); watch {
				return a.runWatch(cmd.Context(), jsonFlag(cmd))
			}
			status, err := a.Manager.Status(cmd.Context())
			if err != nil {
				return err
			}
			if a.Options.Interactive && status.Discovery.State == core.DiscoveryConnecting {
				status, err = a.waitForSettledStatus(cmd.Context(), status)
				if err != nil {
					return err
				}
			}
			if jsonFlag(cmd) {
				return a.writeJSON(status)
			}
			return a.writeStatusHuman(status)
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	command.Flags().Bool("watch", false, "refresh until interrupted")
	return grouped(groupDaily, command)
}

const statusSettleTimeout = 20 * time.Second

func (a *App) waitForSettledStatus(ctx context.Context, initial core.Status) (core.Status, error) {
	fmt.Fprintf(a.Options.Stderr, "Connecting to %s...\n", initial.Host)
	waitCtx, cancel := context.WithTimeout(ctx, statusSettleTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	latest := initial
	for latest.Discovery.State == core.DiscoveryConnecting {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return latest, ctx.Err()
			}
			return latest, nil
		case <-ticker.C:
			status, err := a.Manager.Status(waitCtx)
			if err != nil {
				return latest, err
			}
			latest = status
		}
	}
	return latest, nil
}

func (a *App) watchCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "watch", Short: "refresh live state until interrupted", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runWatch(cmd.Context(), jsonFlag(cmd)) },
	}
	command.Flags().Bool("json", false, "emit one JSON status per change")
	return grouped(groupMore, command)
}

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

func (a *App) managerCommand() *cobra.Command {
	command := &cobra.Command{Use: "manager", Short: "run or recover the background manager", RunE: func(*cobra.Command, []string) error {
		return UsageError(errors.New("manager needs a subcommand (serve, stop, restart)"))
	}}
	command.AddCommand(
		&cobra.Command{Use: "serve", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return a.serveManager(cmd.Context()) }},
		&cobra.Command{Use: "stop", Short: "stop the background manager", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.runManagerStop() }},
		&cobra.Command{Use: "restart", Short: "restart the background manager", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return a.runManagerRestart(cmd.Context()) }},
	)
	return grouped(groupMore, annotateSkipManager(command))
}

func (a *App) runManagerStop() error {
	if err := app.Stop(a.Options.Layout); err != nil {
		return err
	}
	fmt.Fprintln(a.Options.Stdout, "Stopped the manager. Forwards will return on the next command.")
	return nil
}

func (a *App) runManagerRestart(ctx context.Context) error {
	manager, err := app.Restart(ctx, a.connectOptions())
	if err != nil {
		if app.IsResolution(err) {
			return UsageError(err)
		}
		return err
	}
	_ = manager.Close(context.Background())
	fmt.Fprintln(a.Options.Stdout, "Restarted the manager.")
	return nil
}
