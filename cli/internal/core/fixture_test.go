package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	listeners    chan []Listener
	started      chan ForwardTarget
	stopped      chan ForwardTarget
	stopGate     <-chan struct{}
	closed       chan struct{}
	forwardError func(ForwardTarget) error
	closeErr     error
	closeCalls   int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		listeners: make(chan []Listener, 16),
		started:   make(chan ForwardTarget, 16),
		stopped:   make(chan ForwardTarget, 16),
		closed:    make(chan struct{}),
	}
}

func (b *fakeBackend) Observe(ctx context.Context, emit func([]Listener)) error {
	for {
		select {
		case ports := <-b.listeners:
			emit(ports)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *fakeBackend) Forward(ctx context.Context, target ForwardTarget, ready func()) error {
	b.started <- target
	if b.forwardError != nil {
		if err := b.forwardError(target); err != nil {
			return err
		}
	}
	ready()
	<-ctx.Done()
	if b.stopGate != nil {
		<-b.stopGate
	}
	b.stopped <- target
	return ctx.Err()
}

func (b *fakeBackend) Close(context.Context) error {
	b.closeCalls++
	close(b.closed)
	return b.closeErr
}

func samePortForwards(ports ...uint16) []RememberedForward {
	forwards := make([]RememberedForward, 0, len(ports))
	for _, port := range ports {
		forwards = append(forwards, RememberedForward{RemotePort: port, LocalPort: port, AllowFallback: true})
	}
	return forwards
}

func managerStatus(t *testing.T, manager Manager) Status {
	t.Helper()
	status, err := manager.Status(context.Background())
	require.NoError(t, err)
	return status
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func receive(t *testing.T, events <-chan ForwardTarget) ForwardTarget {
	t.Helper()
	select {
	case target := <-events:
		return target
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forward event")
		return ForwardTarget{}
	}
}

func wantEvent(t *testing.T, events <-chan ForwardTarget, port uint16) {
	t.Helper()
	require.Equal(t, port, receive(t, events).RemotePort)
}

func wantTarget(t *testing.T, events <-chan ForwardTarget, target ForwardTarget) {
	t.Helper()
	require.Equal(t, target, receive(t, events))
}

func wantTargets(t *testing.T, events <-chan ForwardTarget, targets ...ForwardTarget) {
	t.Helper()
	got := make([]ForwardTarget, len(targets))
	for i := range got {
		got[i] = receive(t, events)
	}
	require.ElementsMatch(t, targets, got)
}

func wantNoEvent(t *testing.T, events <-chan ForwardTarget) {
	t.Helper()
	select {
	case got := <-events:
		t.Fatalf("unexpected target event %#v", got)
	case <-time.After(20 * time.Millisecond):
	}
}

func testManager(t *testing.T, backend Backend, intent ForwardingIntent, retry ...time.Duration) *manager {
	t.Helper()
	delay := 5 * time.Millisecond
	if len(retry) > 0 {
		delay = retry[0]
	}
	manager := newManager(managerOptions{host: "dev", backend: backend, intent: intent, retryDelay: delay})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

func awaitActive(t *testing.T, manager Manager, targets ...ForwardTarget) Status {
	t.Helper()
	var status Status
	eventually(t, func() bool {
		status = managerStatus(t, manager)
		if len(status.Forwards) != len(targets) {
			return false
		}
		for i, forward := range status.Forwards {
			got := ForwardTarget{Direction: forward.Direction, RemotePort: forward.RemotePort, LocalPort: forward.LocalPort}
			if got != targets[i] || forward.State != ForwardActive {
				return false
			}
		}
		return true
	})
	return status
}

func conflictOnLocalPort(port uint16) func(ForwardTarget) error {
	return func(target ForwardTarget) error {
		if target.Direction == RemoteToLocal && target.LocalPort == port {
			return &BackendError{Diagnostic: "local_port_conflict"}
		}
		return nil
	}
}
