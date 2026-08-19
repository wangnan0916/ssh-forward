package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

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
