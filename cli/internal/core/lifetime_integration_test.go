package core

import (
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"
)

func TestManagerPublishesListenerLifetimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()

		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
			loopback(FamilyIPv4, 8080, "socket:one"),
			loopback(FamilyIPv4, 9090, "socket:two"),
		}}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		want := []ListenerLifetimeSnapshot{
			{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeNew},
			{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 9090, Status: LifetimeNew},
		}
		if got := snapshot.Host.ListenerLifetimes; !cmp.Equal(got, want) {
			t.Fatalf("baseline Listener Lifetimes mismatch (-got +want):\n%s", cmp.Diff(got, want))
		}

		session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
			loopback(FamilyIPv4, 8080, "socket:one"),
			loopback(FamilyIPv4, 9090, "socket:two"),
			loopback(FamilyIPv6, 8080, "socket:six"),
		}}
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		want = []ListenerLifetimeSnapshot{
			{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeContinuous},
			{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 9090, Status: LifetimeContinuous},
			{Family: FamilyIPv6, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeNew, PostBaseline: true},
		}
		if got := snapshot.Host.ListenerLifetimes; !cmp.Equal(got, want) {
			t.Fatalf("second generation Listener Lifetimes mismatch (-got +want):\n%s", cmp.Diff(got, want))
		}
	})
}

func TestManagerPublishesListenerLifetimeReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()

		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
			loopback(FamilyIPv4, 8080, "socket:old"),
		}}
		synctest.Wait()
		session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
			loopback(FamilyIPv4, 8080, "socket:new"),
		}}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		want := []ListenerLifetimeSnapshot{
			{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeReplaced, PostBaseline: true},
		}
		if got := snapshot.Host.ListenerLifetimes; !cmp.Equal(got, want) {
			t.Fatalf("replaced Listener Lifetimes mismatch (-got +want):\n%s", cmp.Diff(got, want))
		}
	})
}

func TestManagerPublishesListenerLifetimeGraceAndEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()

		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{
			loopback(FamilyIPv4, 8080, "socket:one"),
		}}
		synctest.Wait()
		session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{}}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		wantGrace := []ListenerLifetimeSnapshot{
			{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080, Status: LifetimeGrace},
		}
		if got := snapshot.Host.ListenerLifetimes; !cmp.Equal(got, wantGrace) {
			t.Fatalf("grace Listener Lifetimes mismatch (-got +want):\n%s", cmp.Diff(got, wantGrace))
		}
	})
}

// Regression: an unchanged observation set must not freeze an absent
// listener's grace counter — LifetimeEnded must fire past the grace period
// even when nothing else about the snapshot changes, and the listener must
// then drop out of the published lifetimes.
func TestManagerEndsListenerLifetimeWhenObservationSetFrozen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()

		emit := func(sequence uint64, ports ...uint16) {
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
		synctest.Wait()
		assertLifetimeStatus(t, manager, 9090, LifetimeGrace)
		emit(5, 8080) // fourth absent cycle: grace exhausted
		synctest.Wait()
		assertLifetimeStatus(t, manager, 9090, LifetimeEnded)
		emit(6, 8080) // ended listeners drop out of the tracker
		synctest.Wait()
		assertLifetimeAbsent(t, manager, 9090)
	})
}

func assertLifetimeStatus(t *testing.T, manager Manager, port uint16, want LifetimeStatus) {
	t.Helper()
	snapshot, err := manager.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, verdict := range snapshot.Host.ListenerLifetimes {
		if verdict.RemotePort == port {
			if verdict.Status != want {
				t.Fatalf("listener %d status = %q, want %q", port, verdict.Status, want)
			}
			return
		}
	}
	t.Fatalf("listener %d not tracked; want status %q", port, want)
}

func assertLifetimeAbsent(t *testing.T, manager Manager, port uint16) {
	t.Helper()
	snapshot, err := manager.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, verdict := range snapshot.Host.ListenerLifetimes {
		if verdict.RemotePort == port {
			t.Fatalf("listener %d still tracked", port)
		}
	}
}
