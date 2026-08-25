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
				return a.writeStatusJSON(status)
			}
			return a.writeStatusHuman(status)
		},
	}
	command.Flags().Bool("json", false, "emit JSON")
	command.Flags().Bool("watch", false, "refresh until interrupted")
	return grouped(groupDaily, command)
}

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
