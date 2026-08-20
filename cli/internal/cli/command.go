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

// RootCommand builds the cobra command tree: the command definitions and
// help text live here. Connect and Serve live in app so WebUI and CLI share
// them. The global flags — --host, --policies, --ssh-config, --version —
// are persistent flags so they precede any subcommand and appear in help.
func (a *App) RootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ssh-forward",
		Short: "expose Development Host ports locally through system OpenSSH",
		Long: `ssh-forward exposes eligible ports on a Linux Development Host through your system OpenSSH connection, preferring the same port locally.

Name the host with --host ALIAS (-h is help). Pin one with: ssh-forward default ALIAS`,
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
	root.AddGroup(
		&cobra.Group{ID: groupDaily, Title: "Daily:"},
		&cobra.Group{ID: groupHost, Title: "Host:"},
		&cobra.Group{ID: groupMore, Title: "More:"},
	)
	root.PersistentFlags().String("host", "", "Development Host alias (not -h; default: the pinned host)")
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
		a.uiCommand(),
	)
	root.SetHelpCommandGroupID(groupMore)
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
	return grouped(groupDaily, annotateSkipManager(command))
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
	return grouped(groupDaily, annotateSkipManager(command))
}

func (a *App) runRemember(cmd *cobra.Command, args []string, dir string, adding bool) error {
	if a.Options.PoliciesPath == "" {
		return fmt.Errorf("no policies file is configured (--policies)")
	}
	if dir != "" && len(args) != 0 {
		return rememberUsage(cmd.Name())
	}
	jsonOutput := jsonFlag(cmd)
	if dir != "" {
		return a.rememberDir(dir, adding, jsonOutput)
	}
	if len(args) != 1 {
		return rememberUsage(cmd.Name())
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
		if errors.Is(err, app.ErrHostDirectory) {
			return UsageError(fmt.Errorf("%s, for example /home/dev/app", err))
		}
		if errors.Is(err, app.ErrEmptyDirectory) {
			return UsageError(err)
		}
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("directory %s is not remembered", stored)
	}
	return a.writeRemember(jsonOutput, adding, changed, 0, stored)
}

func rememberUsage(name string) error {
	if name == "remove" {
		return UsageError(fmt.Errorf("forget a remote port: ssh-forward remove PORT  or a host directory: ssh-forward remove --dir /home/dev/app"))
	}
	return UsageError(fmt.Errorf("remember a remote port: ssh-forward add PORT  or a host directory: ssh-forward add --dir /home/dev/app"))
}

func (a *App) statusCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "status",
		Short: "show what is forwarded right now",
		Long: `Show the Development Host, whether SSH is connected, and which remote ports are forwarded locally.

Name the host with --host ALIAS (-h is help). Pin one so later commands skip the prompt:

  ssh-forward default ALIAS

On a terminal, status waits until SSH has connected (or failed) and discovery has a first result. --json prints the current snapshot immediately. --watch streams until interrupted (same as ssh-forward watch).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOutput := jsonFlag(cmd)
			if watch, _ := cmd.Flags().GetBool("watch"); watch {
				return a.runWatch(cmd.Context(), jsonOutput)
			}
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
			if a.Options.Interactive {
				snapshot, err = a.waitForSettledStatus(cmd.Context(), snapshot)
				if err != nil {
					return err
				}
			}
			return a.writeStatusHuman(snapshot)
		},
	}
	command.Flags().Bool("json", false, "emit the wire-shaped snapshot")
	command.Flags().Bool("watch", false, "stream live state until interrupted")
	return grouped(groupDaily, command)
}

const statusSettleTimeout = 20 * time.Second

func statusSettled(snap core.Snapshot) bool {
	host := snap.Host
	if host == nil {
		return true
	}
	if host.Connection == core.ConnectionConnecting {
		return false
	}
	if host.Connection != core.ConnectionConnected {
		return true
	}
	return host.Discovery.State != core.DiscoveryStopped && host.Discovery.State != core.DiscoveryStarting
}

func (a *App) waitForSettledStatus(ctx context.Context, initial core.Snapshot) (core.Snapshot, error) {
	if statusSettled(initial) {
		return initial, nil
	}
	if initial.Host != nil && a.Options.Stderr != nil {
		fmt.Fprintf(a.Options.Stderr, "Connecting to %s...\n", initial.Host.Alias)
	}
	waitCtx, cancel := context.WithTimeout(ctx, statusSettleTimeout)
	defer cancel()
	stream, err := a.Manager.Watch(waitCtx)
	if err != nil {
		return initial, nil
	}
	defer stream.Close()
	latest := initial
	for {
		snap, err := stream.Next(waitCtx)
		if err != nil {
			if ctx.Err() != nil && !errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return latest, ctx.Err()
			}
			return latest, nil
		}
		latest = snap
		if statusSettled(snap) {
			return snap, nil
		}
	}
}

func (a *App) watchCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "watch",
		Short: "stream live state until interrupted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runWatch(cmd.Context(), jsonFlag(cmd))
		},
	}
	command.Flags().Bool("json", false, "emit one wire-shaped snapshot per line")
	return grouped(groupMore, command)
}

func (a *App) policyCommand() *cobra.Command {
	runList := func(cmd *cobra.Command, args []string) error {
		return a.runPolicyList(jsonFlag(cmd))
	}
	command := &cobra.Command{
		Use:   "policy",
		Short: "list forwarding policies",
		Args:  cobra.NoArgs,
		RunE:  runList,
	}
	command.PersistentFlags().Bool("json", false, "emit the policies in the file shape")
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list forwarding policies",
		Args:  cobra.NoArgs,
		RunE:  runList,
	})
	return grouped(groupMore, annotateSkipManager(command))
}

func (a *App) hostCommand() *cobra.Command {
	runList := func(cmd *cobra.Command, args []string) error {
		return a.runHostList(jsonFlag(cmd))
	}
	command := &cobra.Command{
		Use:   "host",
		Short: "list Host aliases from ~/.ssh/config",
		Args:  cobra.NoArgs,
		RunE:  runList,
	}
	command.PersistentFlags().Bool("json", false, "emit the host list as JSON")
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list Host aliases from ~/.ssh/config",
		Args:  cobra.NoArgs,
		RunE:  runList,
	})
	return grouped(groupHost, annotateSkipManager(command))
}

func (a *App) defaultCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "default [alias]",
		Short: "show or pin the default Development Host",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.runShowDefault()
			}
			return a.runSetDefault(args[0])
		},
	}
	return grouped(groupHost, annotateSkipManager(command))
}

func (a *App) managerCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "manager",
		Short: "run or recover the singleton manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			return UsageError(fmt.Errorf("manager needs a subcommand (serve, stop, restart)"))
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
	stop := &cobra.Command{
		Use:   "stop",
		Short: "stop the singleton manager",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runManagerStop()
		},
	}
	restart := &cobra.Command{
		Use:   "restart",
		Short: "restart the singleton manager",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runManagerRestart(cmd.Context())
		},
	}
	command.AddCommand(serve, stop, restart)
	return grouped(groupMore, annotateSkipManager(command))
}

func (a *App) runManagerStop() error {
	if err := app.Stop(a.Options.Layout); err != nil {
		return err
	}
	fmt.Fprintln(a.Options.Stdout, "Stopped the manager. Forwards will return on the next status.")
	return nil
}

func (a *App) runManagerRestart(ctx context.Context) error {
	if err := app.Stop(a.Options.Layout); err != nil && !errors.Is(err, app.ErrNotRunning) {
		return err
	}
	session, err := app.Connect(ctx, a.connectOptions())
	if err != nil {
		if app.IsResolution(err) {
			return UsageError(err)
		}
		return err
	}
	_ = session.Manager.Close(context.Background())
	fmt.Fprintln(a.Options.Stdout, "Restarted the manager.")
	return nil
}
