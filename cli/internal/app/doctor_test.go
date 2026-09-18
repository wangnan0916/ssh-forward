package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type doctorBackend struct {
	listeners        []core.Listener
	err              error
	errAfterSnapshot error
}

func (b doctorBackend) Observe(ctx context.Context, emit func([]core.Listener)) error {
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

func TestProbeDiscoveryFirstResult(t *testing.T) {
	listeners := []core.Listener{{Port: 3000}, {Port: 5173, App: "node"}}
	failure := errors.New("SSH failed")
	for _, backend := range []doctorBackend{
		{listeners: listeners}, {listeners: listeners, errAfterSnapshot: failure}, {err: failure}, {},
	} {
		got, err := probeDiscovery(context.Background(), backend)
		require.ErrorIs(t, err, backend.err)
		require.Equal(t, backend.listeners, got)
	}
}

func TestFailedForwardDetailNamesDirectionalListeningEndpoints(t *testing.T) {
	got := failedForwardDetail([]int{15173, 3000}, []int{19222})
	want := "failed local port(s): 3000, 15173; Development Host port(s): 19222"
	require.EqualValuesf(t, want, got, "detail = %q, want %q", got, want)
}

func TestDiagnoseForwardsUsesDirectionalAdvice(t *testing.T) {
	for _, tc := range []struct {
		forward     core.ForwardStatus
		detail, fix string
	}{
		{core.ForwardStatus{Direction: core.LocalToRemote, RemotePort: 19222, Diagnostic: "remote_port_unavailable"}, "failed Development Host port(s): 19222", "Check whether the remote port is occupied and whether sshd allows TCP forwarding."},
		{core.ForwardStatus{Direction: core.RemoteToLocal, LocalPort: 9222, Diagnostic: "local_port_reserved"}, "failed local port(s): 9222", "Choose another --local port or remove one intent."},
	} {
		tc.forward.State = core.ForwardFailed
		got := diagnoseForwards(core.Status{Host: "dev", Forwards: []core.ForwardStatus{tc.forward}})
		require.Equal(t, DoctorCheck{Name: "forwards", State: DoctorFailed, Detail: tc.detail, Fix: tc.fix}, got)
	}
}
