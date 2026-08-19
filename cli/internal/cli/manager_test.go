package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func managerTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("SSH_FORWARD_CONFIG_DIR", t.TempDir())
	layout := app.DefaultLayout()
	return &App{Options: app.Options{Layout: layout}}
}

func TestManagerNeedsSubcommand(t *testing.T) {
	_, err := runCLI(t, managerTestApp(t), "manager")
	if err == nil || !errors.Is(err, ErrUsage) {
		t.Fatalf("manager without subcommand err = %v, want usage", err)
	}
	if !strings.Contains(err.Error(), "serve, stop, restart") {
		t.Fatalf("manager error = %v", err)
	}
}

func TestManagerHelpListsStopRestart(t *testing.T) {
	output, err := runCLI(t, managerTestApp(t), "manager", "--help")
	if err != nil {
		t.Fatalf("manager --help: %v", err)
	}
	for _, want := range []string{"serve", "stop", "restart"} {
		if !strings.Contains(output, want) {
			t.Fatalf("manager --help missing %q:\n%s", want, output)
		}
	}
}

func TestManagerStopWhenNotRunning(t *testing.T) {
	_, err := runCLI(t, managerTestApp(t), "manager", "stop")
	if err == nil || !strings.Contains(err.Error(), "manager is not running") {
		t.Fatalf("stop err = %v, want not running", err)
	}
}
