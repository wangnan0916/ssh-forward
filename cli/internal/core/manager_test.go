package core

import (
	"context"
	"testing"
	"time"
)

type fakeBackend struct {
	listeners chan []Listener
	started   chan uint16
	stopped   chan uint16
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		listeners: make(chan []Listener, 4),
		started:   make(chan uint16, 4),
		stopped:   make(chan uint16, 4),
	}
}

func (b *fakeBackend) Observe(ctx context.Context, _ HostAlias, emit func([]Listener)) error {
	for {
		select {
		case ports := <-b.listeners:
			emit(ports)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *fakeBackend) Forward(ctx context.Context, _ HostAlias, port uint16, ready func()) error {
	b.started <- port
	ready()
	<-ctx.Done()
	b.stopped <- port
	return ctx.Err()
}

func TestManagerForwardsRememberedPortWithoutRemoteListener(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend, ports: []uint16{0, 5173, 5173},
		retryDelay: 5 * time.Millisecond,
	})
	wantEvent(t, backend.started, 5173)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].State == ForwardActive
	})

	wantListener := Listener{Port: 8080, App: "node", WorkingDirectory: "/workspace/app"}
	backend.listeners <- []Listener{wantListener}
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return status.Discovery.State == DiscoveryActive && len(status.Listeners) == 1 &&
			status.Listeners[0] == wantListener
	})

	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, backend.stopped, 5173)
}

func managerStatus(t *testing.T, manager Manager) Status {
	t.Helper()
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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

func wantEvent(t *testing.T, events <-chan uint16, want uint16) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for port %d", want)
	}
}
