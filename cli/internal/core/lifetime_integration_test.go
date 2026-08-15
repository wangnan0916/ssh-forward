package core

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestManagerPublishesListenerLifetimes(t *testing.T) {
	manager, session := newDiscoveryManager(t)

	session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:one"),
		loopback(FamilyIPv4, 9090, "socket:two"),
	}}
	baseline := waitForDiscoveryBaseline(t, manager, true)
	want := []ListenerLifetimeSnapshot{
		{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeNew},
		{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 9090, Status: LifetimeNew},
	}
	if got := baseline.Host.ListenerLifetimes; !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline Listener Lifetimes = %#v, want %#v", got, want)
	}

	session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:one"),
		loopback(FamilyIPv4, 9090, "socket:two"),
		loopback(FamilyIPv6, 8080, "socket:six"),
	}}
	waitForDiscoveryRevision(t, manager, 3)
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want = []ListenerLifetimeSnapshot{
		{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeContinuous},
		{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 9090, Status: LifetimeContinuous},
		{Family: FamilyIPv6, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeNew},
	}
	if got := snapshot.Host.ListenerLifetimes; !reflect.DeepEqual(got, want) {
		t.Fatalf("second generation Listener Lifetimes = %#v, want %#v", got, want)
	}
}

func TestManagerPublishesListenerLifetimeReplacement(t *testing.T) {
	manager, session := newDiscoveryManager(t)

	session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:old"),
	}}
	waitForDiscoveryBaseline(t, manager, true)
	session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:new"),
	}}
	waitForDiscoveryRevision(t, manager, 3)
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := []ListenerLifetimeSnapshot{
		{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeReplaced},
	}
	if got := snapshot.Host.ListenerLifetimes; !reflect.DeepEqual(got, want) {
		t.Fatalf("replaced Listener Lifetimes = %#v, want %#v", got, want)
	}
}

func waitForDiscoveryRevision(t *testing.T, manager Manager, revision Revision) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Revision == revision {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Snapshot revision = %d, want %d; last Snapshot: %#v", snapshot.Revision, revision, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerPublishesListenerLifetimeGraceAndEnd(t *testing.T) {
	manager, session := newDiscoveryManager(t)

	session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:one"),
	}}
	waitForDiscoveryBaseline(t, manager, true)
	session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{}}
	waitForDiscoveryRevision(t, manager, 3)
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Host.ListenerLifetimes; !reflect.DeepEqual(got, []ListenerLifetimeSnapshot{
		{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeGrace},
	}) {
		t.Fatalf("grace Listener Lifetimes = %#v", got)
	}
}

// Regression: an unchanged observation set must not freeze an absent
// listener's grace counter — LifetimeEnded must fire past the grace period
// even when nothing else about the snapshot changes, and the listener must
// then drop out of the published lifetimes.
func TestManagerEndsListenerLifetimeWhenObservationSetFrozen(t *testing.T) {
	manager, session := newDiscoveryManager(t)

	emit := func(sequence uint64, ports ...uint16) {
		t.Helper()
		observations := []ListenerObservation{}
		for _, port := range ports {
			observations = append(observations, loopback(FamilyIPv4, port, SocketIdentity(fmt.Sprintf("socket:%d", port))))
		}
		session.facts <- ObservationSet{Sequence: sequence, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: observations}
	}
	// The facts channel buffers four sets, so sequences 1-4 are delivered
	// before the actor reacts; grace needs three absent cycles, so listener
	// 9090 is still in grace through sequence 4.
	emit(1, 8080, 9090)
	emit(2, 8080)
	emit(3, 8080)
	emit(4, 8080)
	waitForLifetimeStatus(t, manager, 9090, LifetimeGrace)
	emit(5, 8080) // fourth absent cycle: grace exhausted
	waitForLifetimeStatus(t, manager, 9090, LifetimeEnded)
	emit(6, 8080) // ended listeners drop out of the tracker
	waitForLifetimeAbsent(t, manager, 9090)
}

func waitForLifetimeStatus(t *testing.T, manager Manager, port uint16, want LifetimeStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		for _, verdict := range snapshot.Host.ListenerLifetimes {
			if verdict.RemotePort == port && verdict.Status == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener %d never reached %q; last Lifetimes: %#v", port, want, snapshot.Host.ListenerLifetimes)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForLifetimeAbsent(t *testing.T, manager Manager, port uint16) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		found := false
		for _, verdict := range snapshot.Host.ListenerLifetimes {
			if verdict.RemotePort == port {
				found = true
			}
		}
		if !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener %d still tracked; last Lifetimes: %#v", port, snapshot.Host.ListenerLifetimes)
		}
		time.Sleep(time.Millisecond)
	}
}
