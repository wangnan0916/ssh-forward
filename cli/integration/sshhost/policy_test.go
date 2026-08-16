//go:build integration

package sshhost_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

// TestPolicyReconciliationThroughDisposableDevelopmentHost pins the
// composition-root wiring end to end (slice 5): a file-backed policy source
// through app.NewManager must reconcile the disposable host's fixture
// listener into a Managed Forward, and Ignore must suppress the Ask list.
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
	if _, err := manager.Execute(context.Background(), core.AddManualForward{
		CommandID:  core.CommandID("policy-trigger"),
		Host:       core.HostAlias(testHostAlias()),
		RemotePort: fixturePortV4(),
		Family:     core.FamilyIPv4,
	}); err != nil {
		t.Fatalf("start Forwarding Session: %v", err)
	}
	baseline := waitForBaseline(t, manager)

	// The fixture listener reconciles into a Managed Forward after two
	// observation generations, without any user command.
	snapshot := waitForPolicyManagedForward(t, manager, fixturePortV4())
	if baseline.Revision >= snapshot.Revision {
		t.Fatalf("Managed Forward appeared without a revision advance")
	}
	if len(snapshot.Host.AskListeners) != 0 {
		t.Fatalf("auto-forwarded listener still asks: %+v", snapshot.Host.AskListeners)
	}
}

// TestPolicyIgnoreThroughDisposableDevelopmentHost pins the Ignore action:
// a governed listener never asks and never forwards.
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
	if _, err := manager.Execute(context.Background(), core.AddManualForward{
		CommandID:  core.CommandID("ignore-trigger"),
		Host:       core.HostAlias(testHostAlias()),
		RemotePort: fixturePortV4(),
		Family:     core.FamilyIPv4,
	}); err != nil {
		t.Fatalf("start Forwarding Session: %v", err)
	}
	waitForBaseline(t, manager)

	waitForSnapshot(t, manager, "ignored listener settles", func(snapshot core.Snapshot) bool {
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
	// Two more generations settle the reconciliation verdict.
	waitForSnapshot(t, manager, "ignore verdict settles", func(snapshot core.Snapshot) bool {
		if snapshot.Host == nil {
			return false
		}
		for _, listener := range snapshot.Host.AskListeners {
			if listener.RemotePort == fixturePortV4() {
				return false
			}
		}
		return true
	})
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, forward := range snapshot.Host.Forwards {
		if forward.Kind == core.ForwardManaged && forward.RemotePort == fixturePortV4() {
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
			if forward.Kind == core.ForwardManaged && forward.RemotePort == port {
				return true
			}
		}
		return false
	})
}
