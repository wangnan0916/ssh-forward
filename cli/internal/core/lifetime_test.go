package core

import (
	"reflect"
	"testing"
)

func loopback(family AddressFamily, port uint16, identities ...SocketIdentity) ListenerObservation {
	return ListenerObservation{
		Family:           family,
		BindScope:        BindLoopback,
		RemotePort:       port,
		SocketIdentities: identities,
	}
}

func lifetimeStatuses(snapshots []ListenerLifetimeSnapshot) []LifetimeStatus {
	statuses := make([]LifetimeStatus, len(snapshots))
	for index, snapshot := range snapshots {
		statuses[index] = snapshot.Status
	}
	return statuses
}

func TestLifetimeTrackerClassifiesNewAndContinuous(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	first := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if want := []LifetimeStatus{LifetimeNew}; !reflect.DeepEqual(lifetimeStatuses(first), want) {
		t.Fatalf("first generation = %v, want %v", lifetimeStatuses(first), want)
	}
	second := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if want := []LifetimeStatus{LifetimeContinuous}; !reflect.DeepEqual(lifetimeStatuses(second), want) {
		t.Fatalf("second generation = %v, want %v", lifetimeStatuses(second), want)
	}
}

func TestLifetimeTrackerEndsOnFullSocketIdentityReplacement(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:old")})
	replaced := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:new")})
	if want := []LifetimeStatus{LifetimeReplaced}; !reflect.DeepEqual(lifetimeStatuses(replaced), want) {
		t.Fatalf("replacement generation = %v, want %v", lifetimeStatuses(replaced), want)
	}
	following := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:new")})
	if want := []LifetimeStatus{LifetimeContinuous}; !reflect.DeepEqual(lifetimeStatuses(following), want) {
		t.Fatalf("generation after replacement = %v, want %v", lifetimeStatuses(following), want)
	}
}

func TestLifetimeTrackerKeepsContinuityWithoutIdentityEvidence(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	// Identity evidence is unavailable this generation: replacement cannot be
	// concluded, so the Listener stays continuous.
	noEvidence := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080)})
	if want := []LifetimeStatus{LifetimeContinuous}; !reflect.DeepEqual(lifetimeStatuses(noEvidence), want) {
		t.Fatalf("no-evidence generation = %v, want %v", lifetimeStatuses(noEvidence), want)
	}
}

func TestLifetimeTrackerGracePeriodAndReappearance(t *testing.T) {
	tracker := newLifetimeTracker(2)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	firstAbsent := tracker.advance(nil)
	if want := []LifetimeStatus{LifetimeGrace}; !reflect.DeepEqual(lifetimeStatuses(firstAbsent), want) {
		t.Fatalf("first absent generation = %v, want %v", lifetimeStatuses(firstAbsent), want)
	}
	secondAbsent := tracker.advance(nil)
	if want := []LifetimeStatus{LifetimeGrace}; !reflect.DeepEqual(lifetimeStatuses(secondAbsent), want) {
		t.Fatalf("second absent generation = %v, want %v", lifetimeStatuses(secondAbsent), want)
	}
	// Reappearance inside the grace period keeps the lifetime.
	reappeared := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if want := []LifetimeStatus{LifetimeContinuous}; !reflect.DeepEqual(lifetimeStatuses(reappeared), want) {
		t.Fatalf("reappearance generation = %v, want %v", lifetimeStatuses(reappeared), want)
	}
}

func TestLifetimeTrackerEndsAfterGraceAndRestartsAsNew(t *testing.T) {
	tracker := newLifetimeTracker(1)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	tracker.advance(nil)
	ended := tracker.advance(nil)
	if want := []LifetimeStatus{LifetimeEnded}; !reflect.DeepEqual(lifetimeStatuses(ended), want) {
		t.Fatalf("ended generation = %v, want %v", lifetimeStatuses(ended), want)
	}
	restarted := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if want := []LifetimeStatus{LifetimeNew}; !reflect.DeepEqual(lifetimeStatuses(restarted), want) {
		t.Fatalf("generation after end = %v, want %v", lifetimeStatuses(restarted), want)
	}
}

func TestLifetimeTrackerSeparatesListenersByKey(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	tracker.advance([]ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:a"),
		loopback(FamilyIPv6, 8080, "socket:a6"),
	})
	verdicts := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if want := []LifetimeStatus{LifetimeContinuous, LifetimeGrace}; !reflect.DeepEqual(lifetimeStatuses(verdicts), want) {
		t.Fatalf("mixed generation = %v, want %v", lifetimeStatuses(verdicts), want)
	}
}
