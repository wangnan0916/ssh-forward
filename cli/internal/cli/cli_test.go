package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fakeManager struct {
	status core.Status
}

func (m *fakeManager) Status(context.Context) (core.Status, error) { return m.status, nil }
func (*fakeManager) Close(context.Context) error                   { return nil }

func TestAddWritesOneHostPortList(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	var stdout bytes.Buffer
	surface := &App{
		Manager: &fakeManager{status: core.Status{Host: "dev"}},
		Options: app.Options{ConfigPath: configPath, Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"add", "5173"}); err != nil {
		t.Fatal(err)
	}
	ports, err := app.Ports(configPath, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0] != 5173 {
		t.Fatalf("ports = %v", ports)
	}
	if !strings.Contains(stdout.String(), "Remembered 5173 for dev") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestStatusSeparatesForwardedAndAvailablePorts(t *testing.T) {
	var stdout bytes.Buffer
	surface := &App{
		Manager: &fakeManager{status: core.Status{
			Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
			Listeners: []uint16{3000, 5173},
			Forwards:  []core.ForwardStatus{{Port: 5173, State: core.ForwardActive}},
		}},
		Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc", Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, text := range []string{"Host: dev", "5173 → 127.0.0.1:5173", "3000  ssh-forward add 3000"} {
		if !strings.Contains(output, text) {
			t.Fatalf("output = %q, missing %q", output, text)
		}
	}
}

func TestRootHasNoPolicyOrDirectorySurface(t *testing.T) {
	root := (&App{}).RootCommand()
	if _, _, err := root.Find([]string{"policy"}); err == nil {
		t.Fatal("policy command still exists")
	}
	add, _, err := root.Find([]string{"add"})
	if err != nil {
		t.Fatal(err)
	}
	if add.Flags().Lookup("dir") != nil {
		t.Fatal("add --dir still exists")
	}
	status, _, err := root.Find([]string{"status"})
	if err != nil || status.Flags().Lookup("watch") == nil {
		t.Fatal("status --watch is missing")
	}
	if uninstall, _, err := root.Find([]string{"uninstall"}); err != nil || uninstall.Hidden {
		t.Fatalf("uninstall command is missing: %v", err)
	}
	for _, command := range root.Commands() {
		if command.Name() == "watch" || command.Name() == "manager" && !command.Hidden {
			t.Fatalf("obsolete public command %q is still visible", command.Name())
		}
	}
}
