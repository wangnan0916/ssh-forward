package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *App) publishCommand(adding bool) *cobra.Command {
	name, short := "publish", "publish a local port on the Development Host"
	if !adding {
		name, short = "unpublish", "stop publishing a local port"
	}
	command := &cobra.Command{
		Use: name + " LOCAL", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			localPort, err := requirePort(name, "local", args[0])
			if err != nil {
				return UsageError(err)
			}
			forward := core.PublishedForward{LocalPort: localPort}.WithDefaults()
			if adding && cmd.Flags().Changed("remote") {
				remotePort, _ := cmd.Flags().GetUint16("remote")
				if remotePort == 0 {
					return UsageError(errors.New("publish --remote requires a port 1..65535"))
				}
				forward.RemotePort = remotePort
			}
			return a.publishForward(cmd.Context(), forward, adding, jsonFlag(cmd))
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	if adding {
		command.Flags().Uint16("remote", 0, "Development Host port (default: the local port)")
		command.Example = "  ssh-forward publish 9222\n  ssh-forward publish 9222 --remote 19222"
	} else {
		command.Example = "  ssh-forward unpublish 9222"
	}
	return grouped(groupDaily, command)
}

func (a *App) rememberCommand(adding bool) *cobra.Command {
	name, verb, short := "add", "remember", "remember a remote port or working-directory glob"
	if !adding {
		name, verb, short = "remove", "forget", "forget a remembered port or working-directory glob"
	}
	command := &cobra.Command{
		Use: name + " [PORT]", Short: short, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("pwd") {
				if len(args) != 0 {
					return UsageError(fmt.Errorf("%s accepts either PORT or --pwd GLOB, not both", name))
				}
				if adding && cmd.Flags().Changed("local") {
					return UsageError(errors.New("add --pwd cannot be combined with --local"))
				}
				pattern, _ := cmd.Flags().GetString("pwd")
				return a.rememberWorkingDirectory(cmd.Context(), pattern, adding, jsonFlag(cmd))
			}
			if len(args) == 0 {
				return UsageError(fmt.Errorf("%s requires PORT or --pwd GLOB", name))
			}
			remotePort, err := requirePort(name, "remote", args[0])
			if err != nil {
				return UsageError(err)
			}
			forward := core.RememberedForward{RemotePort: remotePort}.WithDefaults()
			if adding && cmd.Flags().Changed("local") {
				localPort, _ := cmd.Flags().GetUint16("local")
				if localPort == 0 {
					return UsageError(errors.New("add --local requires a port 1..65535"))
				}
				forward.LocalPort = localPort
				forward.AllowFallback = false
			}
			return a.rememberForward(cmd.Context(), forward, adding, jsonFlag(cmd))
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	command.Flags().String("pwd", "", "absolute glob for remote process working directories")
	if adding {
		command.Flags().Uint16("local", 0, "local port (default: the remote port)")
	}
	command.Example = fmt.Sprintf("  ssh-forward %s 5173  # %s port 5173\n  ssh-forward %s --pwd '/workspace/**'", name, verb, name)
	if adding {
		command.Example += "\n  ssh-forward add 8443 --local 18443"
	}
	return grouped(groupDaily, command)
}

func (a *App) rememberForward(
	ctx context.Context,
	forward core.RememberedForward,
	adding, jsonOutput bool,
) error {
	status, err := a.Manager.Status(ctx)
	if err != nil {
		return err
	}
	host := string(status.Host)
	var changed bool
	if adding {
		changed, err = app.SetRememberedForward(a.Options.ConfigPath, host, forward)
	} else {
		changed, err = app.RemoveRememberedForward(a.Options.ConfigPath, host, forward.RemotePort)
	}
	if err != nil {
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("remote port %d is not remembered for %s", forward.RemotePort, host)
	}
	if err := a.updateManagerIntent(ctx, host); err != nil {
		return err
	}
	return a.writeRemember(jsonOutput, adding, changed, host, forward)
}

func (a *App) publishForward(
	ctx context.Context,
	forward core.PublishedForward,
	adding, jsonOutput bool,
) error {
	status, err := a.Manager.Status(ctx)
	if err != nil {
		return err
	}
	host := string(status.Host)
	var changed bool
	if adding {
		changed, err = app.SetPublishedForward(a.Options.ConfigPath, host, forward)
	} else {
		intent, loadErr := app.HostIntent(a.Options.ConfigPath, host)
		if loadErr != nil {
			return loadErr
		}
		for _, existing := range intent.PublishedForwards {
			if existing.LocalPort == forward.LocalPort {
				forward = existing
				break
			}
		}
		changed, err = app.RemovePublishedForward(a.Options.ConfigPath, host, forward.LocalPort)
	}
	if err != nil {
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("local port %d is not published for %s", forward.LocalPort, host)
	}
	if err := a.updateManagerIntent(ctx, host); err != nil {
		return err
	}
	return a.writePublished(jsonOutput, adding, changed, host, forward)
}

func (a *App) rememberWorkingDirectory(ctx context.Context, pattern string, adding, jsonOutput bool) error {
	status, err := a.Manager.Status(ctx)
	if err != nil {
		return err
	}
	host := string(status.Host)
	var changed bool
	if adding {
		changed, err = app.AddWorkingDirectoryRule(a.Options.ConfigPath, host, pattern)
	} else {
		changed, err = app.RemoveWorkingDirectoryRule(a.Options.ConfigPath, host, pattern)
	}
	if errors.Is(err, app.ErrInvalidWorkingDirectoryRule) {
		return UsageError(err)
	}
	if err != nil {
		return err
	}
	if !adding && !changed {
		return fmt.Errorf("working-directory glob %q is not remembered for %s", pattern, host)
	}
	if err := a.updateManagerIntent(ctx, host); err != nil {
		return err
	}
	return a.writeRememberWorkingDirectory(jsonOutput, adding, changed, host, pattern)
}

func (a *App) updateManagerIntent(ctx context.Context, host string) error {
	intent, err := app.HostIntent(a.Options.ConfigPath, host)
	if err != nil {
		return err
	}
	return a.Manager.UpdateIntent(ctx, intent)
}
