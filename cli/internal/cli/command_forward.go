package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *App) rememberForward(ctx context.Context, forward core.RememberedForward, adding, jsonOutput bool) error {
	host := a.Options.HostFlag
	changed, err := app.EditRememberedForward(a.Options.ConfigPath, host, &forward, adding)
	if err := a.finishEdit(ctx, adding, changed, err, fmt.Sprintf("remote port %d is not remembered for %s", forward.RemotePort, host)); err != nil {
		return err
	}
	return a.writeRemember(jsonOutput, adding, changed, host, forward)
}

func (a *App) publishForward(ctx context.Context, forward core.PublishedForward, adding, jsonOutput bool) error {
	host := a.Options.HostFlag
	changed, err := app.EditPublishedForward(a.Options.ConfigPath, host, &forward, adding)
	if err := a.finishEdit(ctx, adding, changed, err, fmt.Sprintf("local port %d is not published for %s", forward.LocalPort, host)); err != nil {
		return err
	}
	return a.writePublished(jsonOutput, adding, changed, host, forward)
}

func (a *App) rememberWorkingDirectory(ctx context.Context, pattern string, adding, jsonOutput bool) error {
	host := a.Options.HostFlag
	changed, err := app.EditWorkingDirectoryRule(a.Options.ConfigPath, host, pattern, adding)
	if errors.Is(err, app.ErrInvalidWorkingDirectoryRule) {
		return UsageError(err)
	}
	if err := a.finishEdit(ctx, adding, changed, err, fmt.Sprintf("working-directory glob %q is not remembered for %s", pattern, host)); err != nil {
		return err
	}
	return a.writeRememberWorkingDirectory(jsonOutput, adding, changed, host, pattern)
}

func (a *App) finishEdit(ctx context.Context, adding, changed bool, err error, missing string) error {
	if err != nil {
		return err
	}
	if !adding && !changed {
		return errors.New(missing)
	}
	if err := a.ensureSession(ctx); err != nil {
		return err
	}
	return a.Manager.Reload(ctx, a.Options.HostFlag)
}
