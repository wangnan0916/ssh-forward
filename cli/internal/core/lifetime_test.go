package core

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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
	if got, want := lifetimeStatuses(first), []LifetimeStatus{LifetimeNew}; !cmp.Equal(got, want) {
		t.Fatalf("first generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
	second := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if got, want := lifetimeStatuses(second), []LifetimeStatus{LifetimeContinuous}; !cmp.Equal(got, want) {
		t.Fatalf("second generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
}

func TestLifetimeTrackerEndsOnFullSocketIdentityReplacement(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:old")})
	replaced := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:new")})
	if got, want := lifetimeStatuses(replaced), []LifetimeStatus{LifetimeReplaced}; !cmp.Equal(got, want) {
		t.Fatalf("replacement generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
	following := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:new")})
	if got, want := lifetimeStatuses(following), []LifetimeStatus{LifetimeContinuous}; !cmp.Equal(got, want) {
		t.Fatalf("generation after replacement mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
}

func TestLifetimeTrackerKeepsContinuityWithoutIdentityEvidence(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	// Identity evidence is unavailable this generation: replacement cannot be
	// concluded, so the Listener stays continuous.
	noEvidence := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080)})
	if got, want := lifetimeStatuses(noEvidence), []LifetimeStatus{LifetimeContinuous}; !cmp.Equal(got, want) {
		t.Fatalf("no-evidence generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
}

func TestLifetimeTrackerGracePeriodAndReappearance(t *testing.T) {
	tracker := newLifetimeTracker(2)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	firstAbsent := tracker.advance(nil)
	if got, want := lifetimeStatuses(firstAbsent), []LifetimeStatus{LifetimeGrace}; !cmp.Equal(got, want) {
		t.Fatalf("first absent generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
	secondAbsent := tracker.advance(nil)
	if got, want := lifetimeStatuses(secondAbsent), []LifetimeStatus{LifetimeGrace}; !cmp.Equal(got, want) {
		t.Fatalf("second absent generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
	// Reappearance inside the grace period keeps the lifetime.
	reappeared := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if got, want := lifetimeStatuses(reappeared), []LifetimeStatus{LifetimeContinuous}; !cmp.Equal(got, want) {
		t.Fatalf("reappearance generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
}

func TestLifetimeTrackerEndsAfterGraceAndRestartsAsNew(t *testing.T) {
	tracker := newLifetimeTracker(1)
	tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	tracker.advance(nil)
	ended := tracker.advance(nil)
	if got, want := lifetimeStatuses(ended), []LifetimeStatus{LifetimeEnded}; !cmp.Equal(got, want) {
		t.Fatalf("ended generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
	restarted := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if got, want := lifetimeStatuses(restarted), []LifetimeStatus{LifetimeNew}; !cmp.Equal(got, want) {
		t.Fatalf("generation after end mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
}

func TestLifetimeTrackerSeparatesListenersByKey(t *testing.T) {
	tracker := newLifetimeTracker(defaultListenerGraceCycles)
	tracker.advance([]ListenerObservation{
		loopback(FamilyIPv4, 8080, "socket:a"),
		loopback(FamilyIPv6, 8080, "socket:a6"),
	})
	verdicts := tracker.advance([]ListenerObservation{loopback(FamilyIPv4, 8080, "socket:a")})
	if got, want := lifetimeStatuses(verdicts), []LifetimeStatus{LifetimeContinuous, LifetimeGrace}; !cmp.Equal(got, want) {
		t.Fatalf("mixed generation mismatch (-got +want):\n%s", cmp.Diff(got, want))
	}
}
