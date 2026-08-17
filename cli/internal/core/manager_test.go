package core_test

import (
	"context"
	"testing"

	"ssh-forward/cli/internal/core"

	"github.com/google/go-cmp/cmp"
)

func TestNewManagerSnapshotReturnsInitialState(t *testing.T) {
	manager := core.NewManager()
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})

	got, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot initial manager state: %v", err)
	}
	want := core.Snapshot{Revision: 0}
	if diff := cmp.Diff(got, want); diff != "" {
		t.Fatalf("initial snapshot mismatch (-got +want):\n%s", diff)
	}
}
