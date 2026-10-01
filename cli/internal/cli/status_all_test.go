package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type allHostsManager struct {
	fakeManager
	statuses []core.Status
	calls    int
}

func (m *allHostsManager) AllStatuses(context.Context) ([]core.Status, error) {
	m.calls++
	return m.statuses, nil
}

func TestStatusHostFilterOmitsOtherHosts(t *testing.T) {
	var output bytes.Buffer
	statuses := []core.Status{
		{Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive}},
		{Host: "staging", Discovery: core.DiscoveryStatus{State: core.DiscoveryFailed}},
	}
	manager := &allHostsManager{fakeManager: fakeManager{status: statuses[0]}, statuses: statuses}
	surface := &App{Manager: manager, Options: app.Options{Stdout: &output, ConfigPath: t.TempDir() + "/config.jsonc"}}
	require.NoError(t, surface.Run(t.Context(), []string{"status", "--host", "dev", "--json"}))
	require.Equal(t, 1, manager.calls)
	var status core.Status
	require.NoError(t, json.Unmarshal(output.Bytes(), &status))
	require.Equal(t, core.HostAlias("dev"), status.Host)
	require.NotContains(t, output.String(), "staging")
}
