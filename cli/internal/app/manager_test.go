package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"

	"github.com/google/go-cmp/cmp"
)

// TestInProcessWiresPolicySource pins the composition-root seam: inProcess
// hands FilePolicyReader.Source to NewConfiguredManager. Connection starts
// at construction, so this test uses an isolated SSH config that cannot
// reach a real host.
func TestInProcessWiresPolicySource(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("no ssh in PATH: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(configPath, []byte("Host development\n    HostName 127.0.0.1\n    Port 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "policies": [
	  {"id": "web", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": 8080, "to": 8080}}]}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := inProcess("development", configPath, path)
	if err != nil {
		t.Fatalf("inProcess: %v", err)
	}
	t.Cleanup(func() { _ = session.Manager.Close(context.Background()) })

	snapshot, err := session.Manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Host == nil || snapshot.Host.Alias != core.HostAlias("development") {
		t.Fatalf("snapshot = %+v, want configured host", snapshot)
	}
	loaded, err := LoadPolicies(path)
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	want := []core.ForwardingPolicy{{
		ID:         "web",
		Priority:   10,
		Action:     core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 8080, To: 8080}}},
	}}
	if diff := cmp.Diff(loaded, want); diff != "" {
		t.Fatalf("policies mismatch (-got +want):\n%s", diff)
	}
}

func TestNewOpenSSHAdapterResolvesRelativeConfig(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary")
	}
	adapter, err := NewOpenSSHAdapter("relative/config")
	if err != nil {
		t.Fatalf("NewOpenSSHAdapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("NewOpenSSHAdapter returned nil")
	}
}
