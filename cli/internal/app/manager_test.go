package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fixedManager struct {
	status core.Status
	intent core.ForwardingIntent
}

func TestServiceConfigIsUserScopedAndAutomatic(t *testing.T) {
	config, err := serviceConfig(Options{Layout: Layout{Dir: t.TempDir()}}, "dev", func() {})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"manager", "serve", "--host", "dev"}, config.Arguments); diff != "" {
		t.Fatalf("service arguments mismatch (-want +got):\n%s", diff)
	}
	for _, option := range []string{"UserService", "KeepAlive", "RunAtLoad"} {
		if enabled, _ := config.Option[option].(bool); !enabled {
			t.Fatalf("%s is not enabled", option)
		}
	}
}

func (m *fixedManager) Status(context.Context) (core.Status, error) { return m.status, nil }
func (m *fixedManager) UpdateIntent(_ context.Context, intent core.ForwardingIntent) error {
	m.intent = intent
	return nil
}
func (*fixedManager) Close(context.Context) error { return nil }

func TestManagerIPCRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "manager.sock")
	listener, err := listenManager(path)
	if err != nil {
		t.Fatal(err)
	}
	configPath := writeConfigFile(t, `{"schema_version":5,"default_host":"dev","remembered_forwards":{"dev":[{"remote_port":3000}]}}`)
	pool := &managerPool{configPath: configPath, managers: make(map[string]core.Manager),
		createTarget: func(host string, _ HostTarget, intent core.ForwardingIntent) (core.Manager, error) {
			return &fixedManager{status: core.Status{Host: core.HostAlias(host)}, intent: intent}, nil
		}}
	server := &http.Server{Handler: managerHandler(pool, "test-version")}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = pool.Close(ctx) })
	opts := Options{Layout: Layout{Dir: filepath.Dir(path), Socket: path}, ConfigPath: configPath, Version: "test-version", HostFlag: "user@other"}
	session, err := Connect(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(ctx)
	all, err := session.AllStatuses(ctx)
	if err != nil || len(all) != 2 || all[0].Host != "dev" || all[1].Host != "user@other" {
		t.Fatalf("all statuses: %+v, %v", all, err)
	}
	dev := pool.lookup("dev").(*fixedManager)
	original := dev.intent
	if _, err := SetRememberedForward(configPath, "user@other", core.RememberedForward{RemotePort: 8080}); err != nil {
		t.Fatal(err)
	}
	if err := session.Reload(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(original, dev.intent); diff != "" {
		t.Fatalf("other host changed dev: %s", diff)
	}
	other := pool.lookup("user@other").(*fixedManager)
	if len(other.intent.RememberedForwards) != 1 || other.intent.RememberedForwards[0].RemotePort != 8080 {
		t.Fatalf("configuration not reloaded: %+v", other.intent)
	}
	if err := session.Reload(ctx, "-invalid"); err == nil {
		t.Fatal("invalid host accepted")
	}
	if err := writeTextFile(configPath, `{"schema_version":`); err != nil {
		t.Fatal(err)
	}
	if err := session.Reload(ctx, ""); err == nil {
		t.Fatal("invalid config accepted")
	}
	if pool.lookup("dev") != dev {
		t.Fatal("failed reload replaced runtime")
	}
	if _, err := dialManager(ctx, path, "other-version"); !errors.Is(err, ErrIncompatibleManager) {
		t.Fatalf("version mismatch: %v", err)
	}
}
