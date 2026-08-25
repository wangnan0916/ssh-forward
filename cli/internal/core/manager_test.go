package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBackend struct {
	listeners       chan []Listener
	started         chan ForwardTarget
	stopped         chan ForwardTarget
	closed          chan struct{}
	actualLocalPort uint16
	closeErr        error
	closeCalls      int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		listeners: make(chan []Listener, 4),
		started:   make(chan ForwardTarget, 4),
		stopped:   make(chan ForwardTarget, 4),
		closed:    make(chan struct{}),
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

func (b *fakeBackend) Forward(
	ctx context.Context,
	_ HostAlias,
	target ForwardTarget,
	ready func(uint16),
) error {
	b.started <- target
	localPort := target.LocalPort
	if b.actualLocalPort != 0 {
		localPort = b.actualLocalPort
	}
	ready(localPort)
	<-ctx.Done()
	b.stopped <- target
	return ctx.Err()
}

func (b *fakeBackend) Close(context.Context) error {
	b.closeCalls++
	close(b.closed)
	return b.closeErr
}

func TestManagerForwardsRememberedPortWithoutRemoteListener(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent: ForwardingIntent{RememberedForwards: []RememberedForward{
			{}, {RemotePort: 5173}, {RemotePort: 5173},
		}},
		retryDelay: 5 * time.Millisecond,
	})
	wantTarget(t, backend.started, ForwardTarget{
		RemotePort: 5173, LocalPort: 5173, AllowFallback: true,
	})
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
	wantTarget(t, backend.stopped, ForwardTarget{
		RemotePort: 5173, LocalPort: 5173, AllowFallback: true,
	})
}

func TestManagerCloseClosesBackendOnceAndReturnsItsError(t *testing.T) {
	want := errors.New("close backend")
	backend := newFakeBackend()
	backend.closeErr = want
	manager := newManager(managerOptions{host: "dev", backend: backend})
	if err := manager.Close(context.Background()); !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
	select {
	case <-backend.closed:
	default:
		t.Fatal("backend was not closed")
	}
	if err := manager.Close(context.Background()); !errors.Is(err, want) {
		t.Fatalf("second close error = %v, want %v", err, want)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backend.closeCalls)
	}
}

func TestManagerReconcilesAutomaticForwardForWorkingDirectoryGlob(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent:     ForwardingIntent{WorkingDirectoryRules: []string{"/workspace/app/**"}},
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	backend.listeners <- []Listener{{Port: 3000, WorkingDirectory: "/workspace/application"}}
	eventually(t, func() bool {
		return managerStatus(t, manager).Discovery.State == DiscoveryActive
	})
	wantNoEvent(t, backend.started)

	backend.listeners <- []Listener{{Port: 5173, WorkingDirectory: "/workspace/app/packages/web"}}
	wantEvent(t, backend.started, 5173)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].RemotePort == 5173 &&
			status.Forwards[0].State == ForwardActive && status.Forwards[0].Automatic
	})

	backend.listeners <- nil
	wantEvent(t, backend.stopped, 5173)
	eventually(t, func() bool { return len(managerStatus(t, manager).Forwards) == 0 })
}

func TestRememberedPortOutlivesWorkingDirectoryMatch(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent: ForwardingIntent{
			RememberedForwards:    samePortForwards(5173),
			WorkingDirectoryRules: []string{"/workspace/app/**"},
		},
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	wantEvent(t, backend.started, 5173)
	backend.listeners <- []Listener{{Port: 5173, WorkingDirectory: "/workspace/app"}}
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && !status.Forwards[0].Automatic
	})
	backend.listeners <- nil
	wantNoEvent(t, backend.stopped)
}

func TestAutomaticForwardRestartsOnceAfterRapidListenerReappearance(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent:     ForwardingIntent{WorkingDirectoryRules: []string{"/workspace/**"}},
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	listener := []Listener{{Port: 5173, WorkingDirectory: "/workspace/app"}}

	backend.listeners <- listener
	wantEvent(t, backend.started, 5173)
	backend.listeners <- listener
	wantNoEvent(t, backend.started)

	backend.listeners <- nil
	backend.listeners <- listener
	wantEvent(t, backend.stopped, 5173)
	wantEvent(t, backend.started, 5173)
	wantNoEvent(t, backend.started)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].State == ForwardActive
	})
}

func TestManagerUpdatesRememberedForwardsWithoutRestartingUnchangedForward(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent:     ForwardingIntent{RememberedForwards: samePortForwards(3000)},
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	wantEvent(t, backend.started, 3000)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].State == ForwardActive
	})
	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: samePortForwards(3000, 5173)}); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, backend.started, 5173)
	wantNoEvent(t, backend.stopped)

	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: samePortForwards(5173)}); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, backend.stopped, 3000)
	wantNoEvent(t, backend.started)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].RemotePort == 5173 &&
			status.Forwards[0].State == ForwardActive
	})
}

func TestManagerUpdatesWorkingDirectoryRulesAgainstCurrentListeners(t *testing.T) {
	backend := newFakeBackend()
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	backend.listeners <- []Listener{{Port: 5173, WorkingDirectory: "/workspace/app"}}
	eventually(t, func() bool {
		return managerStatus(t, manager).Discovery.State == DiscoveryActive
	})
	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{
		WorkingDirectoryRules: []string{"/workspace/**"},
	}); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, backend.started, 5173)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].Automatic
	})

	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: samePortForwards(5173)}); err != nil {
		t.Fatal(err)
	}
	wantNoEvent(t, backend.started)
	wantNoEvent(t, backend.stopped)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && !status.Forwards[0].Automatic
	})

	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{}); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, backend.stopped, 5173)
}

func TestManagerRestartsForwardWhenLocalPortChanges(t *testing.T) {
	backend := newFakeBackend()
	initial := RememberedForward{RemotePort: 3000, LocalPort: 13000}
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent:     ForwardingIntent{RememberedForwards: []RememberedForward{initial}},
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	wantTarget(t, backend.started, ForwardTarget(initial))

	updated := RememberedForward{RemotePort: 3000, LocalPort: 14000}
	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{
		RememberedForwards: []RememberedForward{updated},
	}); err != nil {
		t.Fatal(err)
	}
	wantTarget(t, backend.stopped, ForwardTarget(initial))
	wantTarget(t, backend.started, ForwardTarget(updated))
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 && status.Forwards[0].RemotePort == 3000 &&
			status.Forwards[0].LocalPort == 14000 && status.Forwards[0].State == ForwardActive
	})
}

func TestManagerReportsActualFallbackPortWithoutChangingIntent(t *testing.T) {
	backend := newFakeBackend()
	backend.actualLocalPort = 13001
	forward := RememberedForward{
		RemotePort: 3000, LocalPort: 13000, AllowFallback: true,
	}
	manager := newManager(managerOptions{
		host: "dev", backend: backend,
		intent:     ForwardingIntent{RememberedForwards: []RememberedForward{forward}},
		retryDelay: 5 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	wantTarget(t, backend.started, forwardTarget(forward))
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 1 &&
			status.Forwards[0].PreferredLocalPort == 13000 &&
			status.Forwards[0].LocalPort == 13001 &&
			status.Forwards[0].AllowFallback
	})
	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{
		RememberedForwards: []RememberedForward{forward},
	}); err != nil {
		t.Fatal(err)
	}
	status := managerStatus(t, manager).Forwards[0]
	if status.LocalPort != 13001 || status.PreferredLocalPort != 13000 {
		t.Fatalf("status after equivalent update = %#v", status)
	}
	wantNoEvent(t, backend.started)
	wantNoEvent(t, backend.stopped)
}

func samePortForwards(ports ...uint16) []RememberedForward {
	forwards := make([]RememberedForward, 0, len(ports))
	for _, port := range ports {
		forwards = append(forwards, RememberedForward{
			RemotePort: port, LocalPort: port, AllowFallback: true,
		})
	}
	return forwards
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

func wantEvent(t *testing.T, events <-chan ForwardTarget, want uint16) {
	t.Helper()
	select {
	case got := <-events:
		if got.RemotePort != want {
			t.Fatalf("remote port = %d, want %d", got.RemotePort, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for port %d", want)
	}
}

func wantTarget(t *testing.T, events <-chan ForwardTarget, want ForwardTarget) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("target = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for target %#v", want)
	}
}

func wantNoEvent(t *testing.T, events <-chan ForwardTarget) {
	t.Helper()
	select {
	case got := <-events:
		t.Fatalf("unexpected target event %#v", got)
	case <-time.After(20 * time.Millisecond):
	}
}
