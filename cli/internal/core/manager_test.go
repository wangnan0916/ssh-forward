package core

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run equivalent updates, promotion from automatic to fixed intent, removal,
// and remapping against one live runtime so unintended restarts are observable.
func TestManagerIntentLifecycle(t *testing.T) {
	backend := newFakeBackend()
	manager := testManager(t, backend, ForwardingIntent{RememberedForwards: []RememberedForward{{}, {RemotePort: 3000}, {RemotePort: 3000}}})
	first := ForwardTarget{Direction: RemoteToLocal, RemotePort: 3000, LocalPort: 3000}
	wantTarget(t, backend.started, first)
	awaitActive(t, manager, first) // fixed intent works without any listener
	listener := Listener{Port: 5173, App: "node", WorkingDirectory: "/workspace/app"}
	backend.listeners <- []Listener{listener}
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return status.Discovery.State == DiscoveryActive && slices.Equal(status.Listeners, []Listener{listener})
	})
	intent := ForwardingIntent{RememberedForwards: samePortForwards(3000), WorkingDirectoryRules: []string{"/workspace/**"}}
	require.NoError(t, manager.UpdateIntent(context.Background(), intent))
	second := ForwardTarget{Direction: RemoteToLocal, RemotePort: 5173, LocalPort: 5173}
	wantTarget(t, backend.started, second)
	status := awaitActive(t, manager, first, second)
	require.True(t, status.Forwards[1].Automatic)
	wantNoEvent(t, backend.stopped)

	intent.RememberedForwards = samePortForwards(3000, 5173)
	require.NoError(t, manager.UpdateIntent(context.Background(), intent))
	require.False(t, awaitActive(t, manager, first, second).Forwards[1].Automatic)
	backend.listeners <- nil // a promoted fixed forward outlives its listener
	wantNoEvent(t, backend.stopped)
	wantNoEvent(t, backend.started)

	intent.RememberedForwards = samePortForwards(5173)
	require.NoError(t, manager.UpdateIntent(context.Background(), intent))
	wantTarget(t, backend.stopped, first)
	awaitActive(t, manager, second)
	wantNoEvent(t, backend.started)

	mapped := ForwardTarget{Direction: RemoteToLocal, RemotePort: 5173, LocalPort: 15173}
	intent.RememberedForwards = []RememberedForward{{RemotePort: 5173, LocalPort: 15173}}
	require.NoError(t, manager.UpdateIntent(context.Background(), intent))
	wantTarget(t, backend.stopped, second)
	wantTarget(t, backend.started, mapped)
	awaitActive(t, manager, mapped)
	require.NoError(t, manager.Close(context.Background()))
	wantTarget(t, backend.stopped, mapped)
}

func TestManagerCloseClosesBackendOnceAndReturnsItsError(t *testing.T) {
	backend := newFakeBackend()
	backend.closeErr = errors.New("close backend")
	manager := testManager(t, backend, ForwardingIntent{})
	require.ErrorIs(t, manager.Close(context.Background()), backend.closeErr)
	select {
	case <-backend.closed:
	default:
		t.Fatal("backend was not closed")
	}
	require.ErrorIs(t, manager.Close(context.Background()), backend.closeErr)
	require.Equal(t, 1, backend.closeCalls)
}

func TestAutomaticForwardTracksListenerAndRuleRemoval(t *testing.T) {
	backend := newFakeBackend()
	manager := testManager(t, backend, ForwardingIntent{WorkingDirectoryRules: []string{"/workspace/app/**"}})
	backend.listeners <- []Listener{{Port: 3000, WorkingDirectory: "/workspace/application"}}
	eventually(t, func() bool { return managerStatus(t, manager).Discovery.State == DiscoveryActive })
	wantNoEvent(t, backend.started)
	listener := []Listener{{Port: 5173, WorkingDirectory: "/workspace/app/packages/web"}}
	backend.listeners <- listener
	wantEvent(t, backend.started, 5173)
	require.True(t, awaitActive(t, manager, ForwardTarget{Direction: RemoteToLocal, RemotePort: 5173, LocalPort: 5173}).Forwards[0].Automatic)
	backend.listeners <- listener
	wantNoEvent(t, backend.started)
	backend.listeners <- nil
	wantEvent(t, backend.stopped, 5173)
	eventually(t, func() bool { return len(managerStatus(t, manager).Forwards) == 0 })
	backend.listeners <- listener
	wantEvent(t, backend.started, 5173)
	backend.listeners <- nil
	backend.listeners <- listener // reappearance during cleanup must restart exactly once
	wantEvent(t, backend.stopped, 5173)
	wantEvent(t, backend.started, 5173)
	wantNoEvent(t, backend.started)
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{}))
	wantEvent(t, backend.stopped, 5173)
}

func TestManagerReportsActualFallbackPortWithoutChangingIntent(t *testing.T) {
	backend := newFakeBackend()
	backend.forwardError = conflictOnLocalPort(13000)
	forward := RememberedForward{RemotePort: 3000, LocalPort: 13000, AllowFallback: true}
	manager := testManager(t, backend, ForwardingIntent{RememberedForwards: []RememberedForward{forward}})

	preferred := desiredRememberedForward(forward).preferred
	wantTarget(t, backend.started, preferred)
	fallback := preferred
	fallback.LocalPort = 13001
	wantTarget(t, backend.started, fallback)
	status := awaitActive(t, manager, fallback).Forwards[0]
	require.EqualValues(t, 13000, status.PreferredLocalPort)
	require.True(t, status.AllowFallback)
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: []RememberedForward{forward}}))
	status = managerStatus(t, manager).Forwards[0]
	require.Falsef(t, status.LocalPort != 13001 || status.PreferredLocalPort != 13000, "status after equivalent update = %#v", status)
	wantNoEvent(t, backend.started)
	wantNoEvent(t, backend.stopped)
}

func TestManagerPublishesLocalPortAndHidesItsRemoteListener(t *testing.T) {
	backend := newFakeBackend()
	published := PublishedForward{LocalPort: 9222, RemotePort: 19222}
	manager := testManager(t, backend, ForwardingIntent{
		PublishedForwards:     []PublishedForward{published},
		WorkingDirectoryRules: []string{"/workspace/**"},
	})

	wantTarget(t, backend.started, desiredPublishedForward(published).preferred)
	backend.listeners <- []Listener{{Port: published.RemotePort, App: "sshd", WorkingDirectory: "/workspace/app"}}
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return status.Discovery.State == DiscoveryActive && len(status.Listeners) == 0 &&
			len(status.Forwards) == 1 && status.Forwards[0].Direction == LocalToRemote &&
			status.Forwards[0].LocalPort == published.LocalPort &&
			status.Forwards[0].RemotePort == published.RemotePort &&
			status.Forwards[0].PreferredRemotePort == published.RemotePort &&
			status.Forwards[0].State == ForwardActive
	})
	wantNoEvent(t, backend.started)
}

func TestPublishedLocalPortIsSkippedByRememberedFallback(t *testing.T) {
	backend := newFakeBackend()
	backend.forwardError = conflictOnLocalPort(13000)
	remembered := RememberedForward{RemotePort: 3000, LocalPort: 13000, AllowFallback: true}
	published := PublishedForward{LocalPort: 13001, RemotePort: 19001}
	manager := testManager(t, backend, ForwardingIntent{RememberedForwards: []RememberedForward{remembered}, PublishedForwards: []PublishedForward{published}})

	preferred := desiredRememberedForward(remembered).preferred
	fallback := preferred
	fallback.LocalPort = 13002
	wantTargets(t, backend.started, desiredPublishedForward(published).preferred, preferred, fallback)
	awaitActive(t, manager, fallback, desiredPublishedForward(published).preferred)
}

func TestPublishedForwardWaitsForActiveFallbackBindingToStop(t *testing.T) {
	backend := newFakeBackend()
	stopGate := make(chan struct{})
	backend.stopGate = stopGate
	backend.forwardError = conflictOnLocalPort(13000)
	remembered := RememberedForward{RemotePort: 3000, LocalPort: 13000, AllowFallback: true}
	manager := testManager(t, backend, ForwardingIntent{RememberedForwards: []RememberedForward{remembered}})
	closedStopGate := false
	t.Cleanup(func() {
		if !closedStopGate {
			close(stopGate)
		}
		_ = manager.Close(context.Background())
	})

	preferred := desiredRememberedForward(remembered).preferred
	fallback := preferred
	fallback.LocalPort = 13001
	wantTarget(t, backend.started, preferred)
	wantTarget(t, backend.started, fallback)
	awaitActive(t, manager, fallback)

	published := PublishedForward{LocalPort: fallback.LocalPort, RemotePort: 19001}
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: []RememberedForward{remembered}, PublishedForwards: []PublishedForward{published}}))
	wantNoEvent(t, backend.started)
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: []RememberedForward{remembered}, PublishedForwards: []PublishedForward{published}}))
	wantNoEvent(t, backend.started)
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: []RememberedForward{remembered}}))
	for _, forward := range managerStatus(t, manager).Forwards {
		require.Falsef(t, forward.Direction == LocalToRemote, "unpublished wait-only forward remains in status: %#v", forward)
	}
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{RememberedForwards: []RememberedForward{remembered}, PublishedForwards: []PublishedForward{published}}))
	wantNoEvent(t, backend.started)

	close(stopGate)
	closedStopGate = true
	wantTarget(t, backend.stopped, fallback)
	relocated := preferred
	relocated.LocalPort = 13002
	wantTargets(t, backend.started, desiredPublishedForward(published).preferred, preferred, relocated)
	awaitActive(t, manager, relocated, desiredPublishedForward(published).preferred)
}

func TestStrictRememberedForwardFailsOnPublishedLocalPortReservation(t *testing.T) {
	backend := newFakeBackend()
	manager := testManager(t, backend, ForwardingIntent{
		RememberedForwards: []RememberedForward{{RemotePort: 3000, LocalPort: 9222}},
		PublishedForwards:  []PublishedForward{{LocalPort: 9222, RemotePort: 19222}},
	}, time.Second)

	wantTarget(t, backend.started, ForwardTarget{Direction: LocalToRemote, LocalPort: 9222, RemotePort: 19222})
	wantNoEvent(t, backend.started)
	eventually(t, func() bool {
		status := managerStatus(t, manager)
		return len(status.Forwards) == 2 && status.Forwards[0].State == ForwardFailed &&
			status.Forwards[0].Diagnostic == "local_port_reserved" &&
			status.Forwards[1].State == ForwardActive
	})
}
