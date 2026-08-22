package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	listeners chan []uint16
	started   chan uint16
	stopped   chan uint16
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		listeners: make(chan []uint16, 4),
		started:   make(chan uint16, 4),
		stopped:   make(chan uint16, 4),
	}
}

func (b *fakeBackend) Observe(ctx context.Context, _ HostAlias, emit func([]uint16)) error {
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

type mutablePorts struct {
	mu    sync.Mutex
	ports []uint16
	err   error
}

func (s *mutablePorts) read() ([]uint16, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ports), s.err
}

func (s *mutablePorts) set(ports []uint16, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ports, s.err = slices.Clone(ports), err
}

func TestManagerStartsAndStopsRememberedListener(t *testing.T) {
	backend := newFakeBackend()
	ports := &mutablePorts{ports: []uint16{5173}}
	manager := newManager(managerOptions{
		host: "dev", backend: backend, ports: ports.read,
		configPoll: 5 * time.Millisecond, retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := managerStatus(t, manager)
	if len(status.Forwards) != 1 || status.Forwards[0].State != ForwardWaiting {
		t.Fatalf("initial forwards = %#v", status.Forwards)
	}
	backend.listeners <- []uint16{8080, 5173}
	wantEvent(t, backend.started, 5173)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return status.Discovery.State == DiscoveryActive &&
			len(status.Forwards) == 1 && status.Forwards[0].State == ForwardActive
	})

	ports.set(nil, nil)
	wantEvent(t, backend.stopped, 5173)
	eventually(t, func() bool { return len(managerStatus(t, manager).Forwards) == 0 })
}

func TestManagerKeepsLastValidPortsOnConfigError(t *testing.T) {
	backend := newFakeBackend()
	ports := &mutablePorts{ports: []uint16{3000}}
	manager := newManager(managerOptions{
		host: "dev", backend: backend, ports: ports.read,
		configPoll: 5 * time.Millisecond, retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	ports.set(nil, errors.New("bad JSON"))
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return status.ConfigDiagnostic == "config_file_invalid" &&
			len(status.Forwards) == 1 && status.Forwards[0].Port == 3000
	})
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
