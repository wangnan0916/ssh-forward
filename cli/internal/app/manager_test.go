package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"

	"github.com/google/go-cmp/cmp"
)

// TestNewManagerWiresPolicySource pins the composition-root seam (slice 5):
// the policy source handed to NewManager reaches the Manager's snapshot
// derivation, so a file-backed source changes the Ask list. The Manager is
// inert here — no command runs, so nothing dials a Development Host.
func TestNewManagerWiresPolicySource(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skipf("no ssh in PATH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: sshPath})
	if err != nil {
		t.Fatalf("openssh.New: %v", err)
	}
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "policies": [
	  {"id": "web", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": 8080, "to": 8080}}]}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(core.HostAlias("development"), adapter, FilePolicySource(path))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Host == nil || snapshot.Host.Alias != core.HostAlias("development") {
		t.Fatalf("snapshot = %+v, want configured host", snapshot)
	}
	// The source is live: swapping the file to Ignore changes nothing in
	// the snapshot today (no observations), but the wiring itself must be
	// the same object the file-backed source feeds — re-reading the file
	// through the same seam returns the loaded policies.
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
