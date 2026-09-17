package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const statusSettleTimeout = 20 * time.Second

func (a *App) statusCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "status", Short: "show listeners and forwards for all hosts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if watch, _ := cmd.Flags().GetBool("watch"); watch {
				return a.runWatch(cmd.Context(), jsonFlag(cmd))
			}
			status, err := a.readStatuses(cmd.Context())
			if err != nil {
				return err
			}
			if a.Options.Interactive && statusesConnecting(status) {
				status, err = a.waitForSettledStatus(cmd.Context(), status)
				if err != nil {
					return err
				}
			}
			if jsonFlag(cmd) {
				return a.writeStatuses(status, true)
			}
			return a.writeStatuses(status, false)
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	command.Flags().Bool("watch", false, "refresh until interrupted")
	return grouped(groupDaily, command)
}

func (a *App) waitForSettledStatus(ctx context.Context, initial []core.Status) ([]core.Status, error) {
	fmt.Fprintln(a.Options.Stderr, "Waiting for host discovery...")
	waitCtx, cancel := context.WithTimeout(ctx, statusSettleTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	latest := initial
	for statusesConnecting(latest) {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return latest, ctx.Err()
			}
			return latest, nil
		case <-ticker.C:
			status, err := a.readStatuses(waitCtx)
			if err != nil {
				return latest, err
			}
			latest = status
		}
	}
	return latest, nil
}

func statusesConnecting(statuses []core.Status) bool {
	for _, status := range statuses {
		if status.Discovery.State == core.DiscoveryConnecting {
			return true
		}
	}
	return false
}

func (a *App) readStatuses(ctx context.Context) ([]core.Status, error) {
	statuses, err := a.Manager.AllStatuses(ctx)
	if err != nil || a.Options.HostFlag == "" {
		return statuses, err
	}
	for _, status := range statuses {
		if string(status.Host) == a.Options.HostFlag {
			return []core.Status{status}, nil
		}
	}
	return nil, fmt.Errorf("host %s is not monitored", a.Options.HostFlag)
}
