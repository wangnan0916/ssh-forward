package core

import (
	"cmp"
	"slices"
)

// Listener Lifetime: the period during which a Remote Listener retains
// continuity across successful observations, including a short disappearance
// grace period. It ends when the endpoint disappears past the grace period or
// all previously observed Socket Identities are replaced, even if the same
// remote port remains occupied.
//
// The tracker classifies one observation generation per call; grace is counted
// in observation cycles so verdicts are deterministic and clock-free. Policy
// reconciliation (ADR-0015) will consume these verdicts to reconcile Managed
// Forward lifetimes and One-time Approvals.
type LifetimeStatus string

const (
	LifetimeNew        LifetimeStatus = "new"        // first observation of this Listener
	LifetimeContinuous LifetimeStatus = "continuous" // identity evidence persists
	LifetimeReplaced   LifetimeStatus = "replaced"   // port re-occupied by entirely new sockets
	LifetimeGrace      LifetimeStatus = "grace"      // absent, within the disappearance grace period
	LifetimeEnded      LifetimeStatus = "ended"      // absent past the grace period
)

// ListenerLifetimeSnapshot is one Remote Listener's current lifetime status.
type ListenerLifetimeSnapshot struct {
	Family     AddressFamily
	BindScope  ListenerBindScope
	RemotePort uint16
	Status     LifetimeStatus
}

// defaultListenerGraceCycles is the disappearance grace period in observation
// cycles for a Listener with no Socket Identity evidence. The cycle cadence
// is the scanner's scan interval (2s in scanner.sh), so the grace lasts
// roughly 3 × 2s ≈ 6s; core deliberately counts cycles rather than wall time
// to keep verdicts deterministic and clock-free.
const defaultListenerGraceCycles = 3

type listenerLifetime struct {
	identities map[SocketIdentity]struct{}
	grace      int
}

type lifetimeTracker struct {
	graceCycles int
	lifetimes   map[remoteListenerKey]*listenerLifetime
}

func newLifetimeTracker(graceCycles int) *lifetimeTracker {
	return &lifetimeTracker{
		graceCycles: graceCycles,
		lifetimes:   make(map[remoteListenerKey]*listenerLifetime),
	}
}

// advance classifies the current observation generation against the previous
// one and returns one verdict per tracked Listener, current and absent alike,
// ordered deterministically by listener key.
func (t *lifetimeTracker) advance(observations []ListenerObservation) []ListenerLifetimeSnapshot {
	seen := make(map[remoteListenerKey]struct{}, len(observations))
	verdicts := make([]ListenerLifetimeSnapshot, 0, len(observations)+len(t.lifetimes))
	for _, observation := range observations {
		key := listenerKey(observation)
		seen[key] = struct{}{}
		identities := identitySet(observation.SocketIdentities)
		record := t.lifetimes[key]
		switch {
		case record == nil:
			t.lifetimes[key] = &listenerLifetime{identities: identities}
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeNew))
		case len(record.identities) != 0 && len(identities) != 0 && !overlappingIdentities(record.identities, identities):
			t.lifetimes[key] = &listenerLifetime{identities: identities}
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeReplaced))
		default:
			record.identities = identities
			record.grace = 0
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeContinuous))
		}
	}
	for key, record := range t.lifetimes {
		if _, found := seen[key]; found {
			continue
		}
		record.grace++
		if record.grace > t.graceCycles {
			delete(t.lifetimes, key)
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeEnded))
			continue
		}
		verdicts = append(verdicts, lifetimeVerdict(key, LifetimeGrace))
	}
	slices.SortFunc(verdicts, func(left, right ListenerLifetimeSnapshot) int {
		if order := cmp.Compare(left.Family, right.Family); order != 0 {
			return order
		}
		if order := cmp.Compare(left.BindScope, right.BindScope); order != 0 {
			return order
		}
		return cmp.Compare(left.RemotePort, right.RemotePort)
	})
	return verdicts
}

func identitySet(identities []SocketIdentity) map[SocketIdentity]struct{} {
	set := make(map[SocketIdentity]struct{}, len(identities))
	for _, identity := range identities {
		set[identity] = struct{}{}
	}
	return set
}

func overlappingIdentities(previous, current map[SocketIdentity]struct{}) bool {
	for identity := range previous {
		if _, found := current[identity]; found {
			return true
		}
	}
	return false
}

func lifetimeVerdict(key remoteListenerKey, status LifetimeStatus) ListenerLifetimeSnapshot {
	return ListenerLifetimeSnapshot{
		Family:     key.family,
		BindScope:  key.scope,
		RemotePort: key.port,
		Status:     status,
	}
}
