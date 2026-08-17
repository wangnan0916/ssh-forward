//go:build integration

package sshhost_test

import (
	"context"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// The real Development Host is slower than the core suite's one-second
// polling wait, so the integration suite keeps one generic waiter here
// (waitForSnapshot) plus two named wrappers, sharing the same
// deadline/tick/failure-diagnostic convention as the core suite.

func waitForConnected(t *testing.T, manager core.Manager) {
	t.Helper()
	waitForSnapshot(t, manager, "Development Host did not connect", func(snapshot core.Snapshot) bool {
		return snapshot.Host != nil && snapshot.Host.Connection == core.ConnectionConnected
	})
}

func waitForBaseline(t *testing.T, manager core.Manager) core.Snapshot {
	t.Helper()
	return waitForSnapshot(t, manager, "Discovery Baseline did not arrive", func(snapshot core.Snapshot) bool {
		return snapshot.Host != nil && snapshot.Host.Discovery.BaselineEstablished
	})
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
