package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fixedManager struct {
	status core.Status
	intent core.ForwardingIntent
}

func (m *fixedManager) Status(context.Context) (core.Status, error) { return m.status, nil }
func (m *fixedManager) UpdateIntent(_ context.Context, intent core.ForwardingIntent) error {
	m.intent = intent
	return nil
}
func (*fixedManager) Close(context.Context) error { return nil }

func TestManagerIPCRoundTrip(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "manager.sock")
	listener, err := listenManager(path)
	require.NoError(t, err)
	configPath := writeConfigFile(t, `{"schema_version":6,"hosts":{"dev":{"target":"dev"}},"remembered_forwards":{"dev":[{"remote_port":3000}]}}`)
	pool := &managerPool{configPath: configPath, managers: make(map[string]core.Manager),
		createTarget: func(host string, _ HostTarget, intent core.ForwardingIntent) (core.Manager, error) {
			return &fixedManager{status: core.Status{Host: core.HostAlias(host)}, intent: intent}, nil
		}}
	server := &http.Server{Handler: managerHandler(pool, "test-version")}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = pool.Close(context.Background()) })
	opts := Options{Layout: Layout{Dir: filepath.Dir(path), Socket: path}, ConfigPath: configPath, Version: "test-version", HostFlag: "user@other"}
	session, err := Connect(ctx, opts)
	require.NoError(t, err)
	defer session.Close(ctx)
	all, err := session.AllStatuses(ctx)
	require.Falsef(t, err != nil || len(all) != 2 || all[0].Host != "dev" || all[1].Host != "user@other", "all statuses: %+v, %v", all, err)
	dev := pool.lookup("dev").(*fixedManager)
	original := dev.intent
	if _, err := EditRememberedForward(configPath, "user@other", &core.RememberedForward{RemotePort: 8080}, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, session.Reload(ctx, ""))
	require.Equal(t, original, dev.intent)
	other := pool.lookup("user@other").(*fixedManager)
	require.Falsef(t, len(other.intent.RememberedForwards) != 1 || other.intent.RememberedForwards[0].RemotePort != 8080, "configuration not reloaded: %+v", other.intent)
	require.Error(t, session.Reload(ctx, "-invalid"), "invalid host accepted")
	if _, err := dialManager(ctx, path, "other-version"); !errors.Is(err, ErrIncompatibleManager) {
		t.Fatalf("version mismatch: %v", err)
	}
}
