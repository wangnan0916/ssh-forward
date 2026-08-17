//go:build integration

package sshhost_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

// TestPolicyReconciliationThroughDisposableDevelopmentHost pins the
// composition-root wiring end to end (slice 5): a file-backed policy source
// through app.NewManager must reconcile the disposable host's fixture
// listener into a Managed Forward.
func TestPolicyReconciliationThroughDisposableDevelopmentHost(t *testing.T) {
	policiesPath := filepath.Join(t.TempDir(), "policies.jsonc")
	writePolicySource(t, policiesPath, fmt.Sprintf(`{
  "schema_version": 1,
  "policies": [
    {"id": "fixture", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": %d, "to": %d}}]}
  ]
}`, fixturePortV4(), fixturePortV4()))

	adapter, err := openssh.New(openssh.Options{
		Executable:   "/usr/bin/ssh",
		ConfigFile:   isolatedSSHConfig(t),
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("create OpenSSH Adapter: %v", err)
	}
	manager := app.NewManager(core.HostAlias(testHostAlias()), adapter, app.FilePolicySource(policiesPath))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close Manager: %v", err)
		}
	})
	baseline := waitForBaseline(t, manager)

	snapshot := waitForPolicyManagedForward(t, manager, fixturePortV4())
	if baseline.Revision >= snapshot.Revision {
		t.Fatalf("Managed Forward appeared without a revision advance")
	}
}

// TestPolicyIgnoreThroughDisposableDevelopmentHost pins the Ignore action:
// a governed listener is observed and never forwarded.
func TestPolicyIgnoreThroughDisposableDevelopmentHost(t *testing.T) {
	policiesPath := filepath.Join(t.TempDir(), "policies.jsonc")
	writePolicySource(t, policiesPath, fmt.Sprintf(`{
  "schema_version": 1,
  "policies": [
    {"id": "ignore-fixture", "priority": 10, "action": "ignore", "conditions": [{"remote_ports": {"from": %d, "to": %d}}]}
  ]
}`, fixturePortV4(), fixturePortV4()))

	adapter, err := openssh.New(openssh.Options{
		Executable:   "/usr/bin/ssh",
		ConfigFile:   isolatedSSHConfig(t),
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("create OpenSSH Adapter: %v", err)
	}
	manager := app.NewManager(core.HostAlias(testHostAlias()), adapter, app.FilePolicySource(policiesPath))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close Manager: %v", err)
		}
	})
	waitForBaseline(t, manager)

	first := waitForSnapshot(t, manager, "ignored listener settles", func(snapshot core.Snapshot) bool {
		if snapshot.Host == nil {
			return false
		}
		for _, listener := range snapshot.Host.ListenerObservations {
			if listener.RemotePort == fixturePortV4() {
				return true
			}
		}
		return false
	})
	waitForSnapshot(t, manager, "second generation after ignore", func(snapshot core.Snapshot) bool {
		return snapshot.Revision > first.Revision
	})
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, forward := range snapshot.Host.Forwards {
		if forward.RemotePort == fixturePortV4() {
			t.Fatalf("Ignored listener created a Managed Forward: %+v", forward)
		}
	}
}

func writePolicySource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitForPolicyManagedForward waits until the fixture port has a Managed
// Forward from reconciliation (the create side needs two observations).
func waitForPolicyManagedForward(t *testing.T, manager core.Manager, port uint16) core.Snapshot {
	t.Helper()
	return waitForSnapshot(t, manager, fmt.Sprintf("Managed Forward for port %d", port), func(snapshot core.Snapshot) bool {
		if snapshot.Host == nil {
			return false
		}
		for _, forward := range snapshot.Host.Forwards {
			if forward.RemotePort == port {
				return true
			}
		}
		return false
	})
}
