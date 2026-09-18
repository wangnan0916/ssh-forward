package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type allHostsManager struct {
	fakeManager
	statuses []core.Status
	calls    int
	cancel   context.CancelFunc
}

func (m *allHostsManager) AllStatuses(context.Context) ([]core.Status, error) {
	m.calls++
	if m.cancel != nil {
		m.cancel()
	}
	return m.statuses, nil
}

func TestStatusDisplaysAllHostsAndExplicitHostFilters(t *testing.T) {
	for _, mode := range []string{"human", "json", "filtered", "watch"} {
		t.Run(mode, func(t *testing.T) {
			var output bytes.Buffer
			statuses := []core.Status{
				{Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive}},
				{Host: "staging", Discovery: core.DiscoveryStatus{State: core.DiscoveryFailed, Diagnostic: "transport_unavailable"}},
			}
			manager := &allHostsManager{fakeManager: fakeManager{status: statuses[0]}, statuses: statuses}
			surface := &App{Manager: manager, Options: app.Options{Stdout: &output, ConfigPath: t.TempDir() + "/config.jsonc"}}
			args := []string{"status"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "json":
				args = append(args, "--json")
			case "filtered":
				args = append(args, "--host", "dev", "--json")
			case "watch":
				args = append(args, "--watch", "--json")
				manager.cancel = cancel
			}
			require.NoError(t, surface.Run(ctx, args))
			if mode == "filtered" {
				require.Falsef(t, manager.calls != 1 || strings.Contains(output.String(), "staging"), "host filter ignored: %s", output.String())
				var status statusJSONOutput
				require.NoError(t, json.Unmarshal(output.Bytes(), &status))
				return
			}
			require.Falsef(t, manager.calls != 1 || !strings.Contains(output.String(), "dev") || !strings.Contains(output.String(), "staging"), "missing hosts: %s", output.String())
			if mode != "human" {
				var got []statusJSONOutput
				require.NoError(t, json.Unmarshal(output.Bytes(), &got))
				require.Falsef(t, len(got) != 2 || got[1].Discovery.State != core.DiscoveryFailed, "lost offline host: %+v", got)
			}
		})
	}
}

func TestGlobalRuleCommandsIgnoreDefaultHost(t *testing.T) {
	path := t.TempDir() + "/config.jsonc"
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":5,"default_host":"dev"}`), 0600))
	for _, args := range [][]string{{"add", "8080"}, {"add", "--pwd", "/workspace/**"}} {
		var out bytes.Buffer
		surface := &App{Manager: &fakeManager{status: core.Status{Host: "dev"}}, Options: app.Options{ConfigPath: path, Stdout: &out}}
		require.NoError(t, surface.Run(context.Background(), args))
		require.Contains(t, out.String(), "all hosts")
	}
	for _, host := range []string{"dev", "another"} {
		intent, err := app.HostIntent(path, host)
		require.NoError(t, err)
		require.Falsef(t, len(intent.AutoForwards) != 1 || len(intent.WorkingDirectoryRules) != 1 || len(intent.RememberedForwards) != 0, "not global for %s: %+v", host, intent)
	}
	surface := &App{Manager: &fakeManager{}, Options: app.Options{ConfigPath: path}}
	require.NoError(t, surface.Run(context.Background(), []string{"remove", "8080"}))
	intent, err := app.HostIntent(path, "another")
	require.NoError(t, err)
	require.Falsef(t, len(intent.AutoForwards) != 0 || len(intent.WorkingDirectoryRules) != 1, "remove leaked: %+v", intent)
}

func TestPublishRequiresExplicitHostEvenWithDefault(t *testing.T) {
	path := t.TempDir() + "/config.jsonc"
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":5,"default_host":"dev"}`), 0600))
	surface := &App{Manager: &fakeManager{}, Options: app.Options{ConfigPath: path}}
	if err := surface.Run(context.Background(), []string{"publish", "9222"}); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("publish error: %v", err)
	}
}
