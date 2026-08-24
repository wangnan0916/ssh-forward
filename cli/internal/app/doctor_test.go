package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type doctorBackend struct {
	listeners        []core.Listener
	err              error
	errAfterSnapshot error
}

func (b doctorBackend) Observe(ctx context.Context, _ core.HostAlias, emit func([]core.Listener)) error {
	if b.err != nil {
		return b.err
	}
	emit(b.listeners)
	if b.errAfterSnapshot != nil {
		return b.errAfterSnapshot
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestProbeDiscoveryReturnsFirstCompleteSnapshot(t *testing.T) {
	want := []core.Listener{{Port: 3000}, {Port: 5173, App: "node"}}
	got, err := probeDiscovery(context.Background(), doctorBackend{listeners: want}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("listeners = %#v, want %#v", got, want)
	}
}

func TestProbeDiscoveryReportsBackendFailure(t *testing.T) {
	want := &core.BackendError{Diagnostic: "authentication_failed"}
	_, err := probeDiscovery(context.Background(), doctorBackend{err: want}, "dev")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestProbeDiscoveryPrefersSnapshotOverLaterFailure(t *testing.T) {
	want := []core.Listener{{Port: 3000}}
	wantErr := errors.New("connection closed after snapshot")
	backend := doctorBackend{listeners: want, errAfterSnapshot: wantErr}

	got, err := probeDiscovery(context.Background(), backend, "dev")
	if err != nil {
		t.Fatalf("error = %v after a complete snapshot", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("listeners = %#v, want %#v", got, want)
	}
}
