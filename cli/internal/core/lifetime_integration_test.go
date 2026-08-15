package core

import (
	"context"
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
