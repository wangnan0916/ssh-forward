package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const statusSettleTimeout = 20 * time.Second

func (c *statusCommand) Run(a *App, ctx context.Context) error {
	if err := a.ensureSession(ctx); err != nil {
		return err
	}
	if c.Watch {
		return a.runWatch(ctx, c.JSON)
	}
	statuses, err := a.readStatuses(ctx)
	if err != nil {
		return err
	}
	if a.Options.Interactive && statusesConnecting(statuses) {
		statuses, err = a.waitForSettledStatus(ctx, statuses)
		if err != nil {
			return err
		}
	}
	return a.writeStatuses(statuses, c.JSON)
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
