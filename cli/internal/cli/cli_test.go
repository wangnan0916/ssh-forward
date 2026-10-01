package cli

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fakeManager struct {
	status  core.Status
	reloads []string
}

func (m *fakeManager) AllStatuses(context.Context) ([]core.Status, error) {
	return []core.Status{m.status}, nil
}
func (m *fakeManager) Reload(_ context.Context, host string) error {
	m.reloads = append(m.reloads, host)
	return nil
}
func (*fakeManager) Close(context.Context) error { return nil }

func TestUnpublishRemovesMappingAndReloads(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	if _, err := app.EditPublishedForward(configPath, "dev", &core.PublishedForward{LocalPort: 9222, RemotePort: 19222}, true); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	surface := &App{Manager: manager, Options: app.Options{ConfigPath: configPath, Stdout: &stdout}}
	require.NoError(t, surface.Run(t.Context(), []string{"unpublish", "9222", "--json", "--host", "dev"}))
	intent, err := app.HostIntent(configPath, "dev")
	require.Falsef(t, err != nil || len(intent.PublishedForwards) != 0, "saved intent: %+v, %v", intent, err)
	require.EqualValues(t, 1, len(manager.reloads), "manager was not reloaded")
}

func TestIncompleteCommandsReturnUsage(t *testing.T) {
	for _, args := range [][]string{{"policy"}, {"watch"}, {"add", "--dir", "/workspace"}, {"remove", "5173", "--local", "15173"}, {"unpublish", "9222", "--remote", "19222"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			surface, manager, _ := testCLI(t)
			require.ErrorIs(t, surface.Run(t.Context(), args), ErrUsage)
			require.Empty(t, manager.reloads)
			_, err := os.Stat(surface.Options.ConfigPath)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func testCLI(t *testing.T) (*App, *fakeManager, *bytes.Buffer) {
	t.Helper()
	output := new(bytes.Buffer)
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	return &App{Manager: manager, Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc", Stdout: output}}, manager, output
}

func TestHostCommandsPersistSettingsAcrossIgnoreAndEnable(t *testing.T) {
	surface, _, _ := testCLI(t)
	require.NoError(t, surface.Run(t.Context(), []string{"host", "add", "dev", "--target", "me@dev", "--port", "2222", "--user", "me", "--identity", "/keys/dev", "--jump", "jump", "--ssh-config", "/ssh/config"}))
	hosts, _, err := app.HostList(surface.Options.ConfigPath)
	require.NoError(t, err)
	target := hosts["dev"]
	require.Equal(t, "me@dev", target.Target)
	for _, action := range []string{"ignore", "enable"} {
		require.NoError(t, surface.Run(t.Context(), []string{"host", action, "dev"}))
		hosts, ignored, err := app.HostList(surface.Options.ConfigPath)
		require.NoError(t, err)
		require.Equal(t, target, hosts["dev"])
		require.Equal(t, action == "ignore", slices.Contains(ignored, "dev"))
	}
}
