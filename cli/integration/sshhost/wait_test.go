//go:build integration

package sshhost_test

import (
	"context"
	"testing"
	"time"

	"ssh-forward/cli/internal/core"
)

// The real Development Host is slower than the core suite's one-second
// polling wait, so the integration suite keeps its own two waiters here,
// sharing the same deadline/tick/failure-diagnostic convention.

func waitForConnected(t *testing.T, manager core.Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Manager Snapshot: %v", err)
		}
		if snapshot.Host != nil && snapshot.Host.Connection == core.ConnectionConnected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Development Host did not connect; last Snapshot: %#v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForBaseline(t *testing.T, manager core.Manager) core.Snapshot {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Host != nil && snapshot.Host.Discovery.BaselineEstablished {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Discovery Baseline did not arrive; last Snapshot: %#v", snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForSnapshot polls until cond holds, with the same deadline/tick/
// failure-diagnostic convention as the core suite's waiter.
func waitForSnapshot(t *testing.T, manager core.Manager, describe string, cond func(core.Snapshot) bool) core.Snapshot {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if cond(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s; last Snapshot: %#v", describe, snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
