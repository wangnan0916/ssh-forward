package core_test

import (
	"context"
	"reflect"
	"testing"

	"ssh-forward/cli/internal/core"
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
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initial snapshot = %#v, want %#v", got, want)
	}
}
