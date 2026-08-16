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
// PostBaseline records whether the Listener was first observed after the
// Discovery Baseline: only post-baseline Listeners enter the Ask flow.
type ListenerLifetimeSnapshot struct {
	Family       AddressFamily
	BindScope    ListenerBindScope
	RemotePort   uint16
	Status       LifetimeStatus
	PostBaseline bool
}

// defaultListenerGraceCycles is the disappearance tolerance for a Listener
// with no Socket Identity evidence, in observation cycles. A Listener ends
// after graceCycles+1 consecutive observations without it — 3 full cycles
// of tolerance plus the ending scan, i.e. 4 consecutive absences (about 8s
// at the scanner's 2s cadence in scanner.sh). Core deliberately counts
// cycles rather than wall time to keep verdicts deterministic and clock-free.
const defaultListenerGraceCycles = 3

type listenerLifetime struct {
	identities   map[SocketIdentity]struct{}
	grace        int
	postBaseline bool
}

type lifetimeTracker struct {
	graceCycles int
	baseline    bool
	lifetimes   map[remoteListenerKey]*listenerLifetime
}

func newLifetimeTracker(graceCycles int) *lifetimeTracker {
	return &lifetimeTracker{
		graceCycles: graceCycles,
		lifetimes:   make(map[remoteListenerKey]*listenerLifetime),
	}
}

// markBaseline records that the Discovery Baseline is established: Listeners
// first observed from here on enter the Ask flow. Baseline is established by
// the actor on the first complete ObservationSet (actor.go); the tracker
// stays clock-free and knows nothing about sessions or outages.
func (t *lifetimeTracker) markBaseline() {
	t.baseline = true
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
			t.lifetimes[key] = &listenerLifetime{identities: identities, postBaseline: t.baseline}
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeNew, t.baseline))
		case len(record.identities) != 0 && len(identities) != 0 && !overlappingIdentities(record.identities, identities):
			t.lifetimes[key] = &listenerLifetime{identities: identities, postBaseline: true}
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeReplaced, true))
		default:
			record.identities = identities
			record.grace = 0
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeContinuous, record.postBaseline))
		}
	}
	for key, record := range t.lifetimes {
		if _, found := seen[key]; found {
			continue
		}
		record.grace++
		if record.grace > t.graceCycles {
			delete(t.lifetimes, key)
			verdicts = append(verdicts, lifetimeVerdict(key, LifetimeEnded, record.postBaseline))
			continue
		}
		verdicts = append(verdicts, lifetimeVerdict(key, LifetimeGrace, record.postBaseline))
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

func lifetimeVerdict(key remoteListenerKey, status LifetimeStatus, postBaseline bool) ListenerLifetimeSnapshot {
	return ListenerLifetimeSnapshot{
		Family:       key.family,
		BindScope:    key.scope,
		RemotePort:   key.port,
		Status:       status,
		PostBaseline: postBaseline,
	}
}
