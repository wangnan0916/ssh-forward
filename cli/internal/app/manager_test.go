package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fixedManager struct {
	status core.Status
	intent core.ForwardingIntent
}

func TestServiceConfigIsUserScopedAndAutomatic(t *testing.T) {
	config := serviceConfig(Options{Layout: Layout{Dir: t.TempDir()}}, "dev", func() {})
	if !slices.Equal(config.Arguments, []string{"manager", "serve", "--host", "dev"}) {
		t.Fatalf("arguments = %v", config.Arguments)
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
	path := filepath.Join(t.TempDir(), "manager.sock")
	listener, err := listenManager(path)
	if err != nil {
		t.Fatal(err)
	}
	want := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []core.Listener{{Port: 5173, App: "node", WorkingDirectory: "/workspace/app"}},
		Forwards: []core.ForwardStatus{{
			RemotePort: 5173, PreferredLocalPort: 5173, LocalPort: 5173,
			State: core.ForwardActive, Automatic: true, AllowFallback: true,
		}},
		WorkingDirectoryRules: []string{"/workspace/**"},
	}
	backend := &fixedManager{status: want}
	server := &http.Server{Handler: managerHandler(backend, "test-version")}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	manager, err := dialManager(context.Background(), path, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	got, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
	wantIntent := core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{
			{RemotePort: 3000, LocalPort: 13000},
			{RemotePort: 5173, LocalPort: 5173},
		},
		WorkingDirectoryRules: []string{"/workspace/**"},
	}
	if err := manager.UpdateIntent(context.Background(), wantIntent); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.intent, wantIntent) {
		t.Fatalf("intent = %#v, want %#v", backend.intent, wantIntent)
	}
	if _, err := dialManager(context.Background(), path, "other-version"); !errors.Is(err, ErrIncompatibleManager) {
		t.Fatalf("version mismatch error = %v", err)
	}
}

func TestManagerMatchesSelectedHostAndForwardingIntent(t *testing.T) {
	status := core.Status{
		Host:                  "dev",
		WorkingDirectoryRules: []string{"/workspace/**"},
		Forwards: []core.ForwardStatus{
			{RemotePort: 3000, PreferredLocalPort: 13000, LocalPort: 13001, State: core.ForwardActive, AllowFallback: true},
			{RemotePort: 5173, PreferredLocalPort: 5173, LocalPort: 5173, State: core.ForwardFailed},
			{RemotePort: 12000, PreferredLocalPort: 12000, LocalPort: 12000, State: core.ForwardActive, Automatic: true, AllowFallback: true},
		},
	}
	intent := core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{
			{RemotePort: 3000, LocalPort: 13000, AllowFallback: true},
			{RemotePort: 5173, LocalPort: 5173},
		},
		WorkingDirectoryRules: []string{"/workspace/**"},
	}
	if !managerMatches(status, "dev", intent) {
		t.Fatal("matching manager was rejected")
	}
	if managerMatches(status, "other", intent) {
		t.Fatal("manager with another host was accepted")
	}
	intent.RememberedForwards = intent.RememberedForwards[:1]
	if managerMatches(status, "dev", intent) {
		t.Fatal("manager with stale ports was accepted")
	}
	intent.RememberedForwards = append(intent.RememberedForwards, core.RememberedForward{
		RemotePort: 5173, LocalPort: 5173,
	})
	intent.RememberedForwards[0].LocalPort = 14000
	if managerMatches(status, "dev", intent) {
		t.Fatal("manager with a stale local port was accepted")
	}
	intent.RememberedForwards[0].LocalPort = 13000
	intent.WorkingDirectoryRules = []string{"/srv/**"}
	if managerMatches(status, "dev", intent) {
		t.Fatal("manager with stale working-directory rules was accepted")
	}
}
