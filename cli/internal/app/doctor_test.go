package app

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
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
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("listeners mismatch (-want +got):\n%s", diff)
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
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("listeners mismatch (-want +got):\n%s", diff)
	}
}

func TestFailedForwardDetailNamesDirectionalListeningEndpoints(t *testing.T) {
	got := failedForwardDetail([]int{15173, 3000}, []int{19222})
	want := "failed local port(s): 3000, 15173; Development Host port(s): 19222"
	if got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
}

func TestDiagnoseForwardsUsesPublishedDiagnosticAdvice(t *testing.T) {
	got := diagnoseForwards(core.Status{
		Host: "dev",
		Forwards: []core.ForwardStatus{{
			Direction:  core.LocalToRemote,
			RemotePort: 19222,
			State:      core.ForwardFailed,
			Diagnostic: "remote_port_unavailable",
		}},
	})
	want := DoctorCheck{
		Name:   "forwards",
		State:  DoctorFailed,
		Detail: "failed Development Host port(s): 19222",
		Fix:    "Check whether the remote port is occupied and whether sshd allows TCP forwarding.",
	}
	if got != want {
		t.Fatalf("check = %#v, want %#v", got, want)
	}
}

func TestDiagnoseForwardsUsesReservedLocalPortAdvice(t *testing.T) {
	got := diagnoseForwards(core.Status{
		Host: "dev",
		Forwards: []core.ForwardStatus{{
			Direction:  core.RemoteToLocal,
			LocalPort:  9222,
			State:      core.ForwardFailed,
			Diagnostic: "local_port_reserved",
		}},
	})
	want := DoctorCheck{
		Name:   "forwards",
		State:  DoctorFailed,
		Detail: "failed local port(s): 9222",
		Fix:    "Choose another --local port or remove one intent.",
	}
	if got != want {
		t.Fatalf("check = %#v, want %#v", got, want)
	}
}
