package app

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fixedManager struct {
	status core.Status
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

func (m fixedManager) Status(context.Context) (core.Status, error) { return m.status, nil }
func (fixedManager) Close(context.Context) error                   { return nil }

func TestManagerStatusRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sock")
	listener, err := listenManager(path)
	if err != nil {
		t.Fatal(err)
	}
	want := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Forwards:  []core.ForwardStatus{{Port: 5173, State: core.ForwardActive}},
	}
	server := &http.Server{Handler: managerHandler(fixedManager{status: want})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	manager, err := dialManager(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	got, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != want.Host || len(got.Forwards) != 1 || got.Forwards[0] != want.Forwards[0] {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
}

func TestManagerMatchesSelectedHostAndConfiguredPorts(t *testing.T) {
	status := core.Status{
		Host: "dev",
		Forwards: []core.ForwardStatus{
			{Port: 3000, State: core.ForwardActive},
			{Port: 5173, State: core.ForwardFailed},
		},
	}
	if !managerMatches(status, "dev", []uint16{3000, 5173}) {
		t.Fatal("matching manager was rejected")
	}
	if managerMatches(status, "other", []uint16{3000, 5173}) {
		t.Fatal("manager with another host was accepted")
	}
	if managerMatches(status, "dev", []uint16{3000}) {
		t.Fatal("manager with stale ports was accepted")
	}
}
