package core

import (
	"context"
	"testing"
	"time"
)

// waitForSnapshot polls manager.Snapshot until cond accepts it, failing the
// test with describe and the last snapshot after one second. Every async
// core test waits through this single helper so the polling mechanics and
// the failure-diagnostic convention live in one place; each test keeps its
// own predicate and describes its own expectation.
func waitForSnapshot(t *testing.T, manager Manager, describe string, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
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
		time.Sleep(time.Millisecond)
	}
}
