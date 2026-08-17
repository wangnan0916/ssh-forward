package core

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeClock is the injected wall clock for the reconciliation path: the
// five-second removal floor is the one place wall time reaches the
// deliberately clock-free core (decision recorded in
// implementation-sequence.md slice 5).
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

// policyRef is a mutable policy source: the reconciliation path reads the
// current set on every observation, so tests can swap policies mid-run.
type policyRef struct {
	mu       sync.Mutex
	policies []ForwardingPolicy
}

func (r *policyRef) snapshot() []ForwardingPolicy {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ForwardingPolicy(nil), r.policies...)
}

func (r *policyRef) set(policies []ForwardingPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies = append([]ForwardingPolicy(nil), policies...)
}

// autoAllocator answers every allocation immediately with a working
// projection, recording the specs it was asked for.
type autoAllocator struct {
	mu       sync.Mutex
	requests []forwardSpec
	next     uint16
}

func (a *autoAllocator) Allocate(_ context.Context, spec forwardSpec) (ownedForward, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, spec)
	return &simpleOwnedForward{projection: ForwardSnapshot{
		ID:                 spec.ID,
		Kind:               spec.Kind,
		RemotePort:         spec.Remote.Port(),
		RemoteFamily:       familyForAddress(spec.Remote.Addr()),
		AllocatedLocalPort: spec.PreferredLocalPort,
		LocalFamilies:      []AddressFamily{FamilyIPv4},
	}}, nil
}

func (a *autoAllocator) allocated() []forwardSpec {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]forwardSpec(nil), a.requests...)
}

type simpleOwnedForward struct {
	projection ForwardSnapshot
}

func (f *simpleOwnedForward) Projection() ForwardSnapshot { return cloneForward(f.projection) }
func (f *simpleOwnedForward) Close(context.Context) error { return nil }

// reconcileHarness wires a Manager with scripted discovery, a mutable
// policy source, an auto allocator, and the injected clock. The actor is
// armed at construction.
type reconcileHarness struct {
	t        *testing.T
	manager  *manager
	session  *scriptedDiscoverySession
	clock    *fakeClock
	policies *policyRef
	alloc    *autoAllocator
	sequence uint64
}

func newReconcileHarness(t *testing.T, policies []ForwardingPolicy) *reconcileHarness {
	t.Helper()
	session := newScriptedDiscoverySession()
	connector := &sequenceConnector{
		sessions: []hostSession{session},
		releases: []<-chan struct{}{closedChannel()},
		started:  make(chan int, 1),
	}
	clock := newFakeClock()
	ref := &policyRef{policies: policies}
	alloc := &autoAllocator{}
	manager := newManager(managerOptions{
		host:       HostAlias("development"),
		connector:  connector,
		retryDelay: func(int) time.Duration { return 0 },
		retryWait: func(ctx context.Context, _ time.Duration) bool {
			return ctx.Err() == nil
		},
		forwardAllocator: alloc,
		policies:         ref.snapshot,
		now:              clock.current,
	})
	manager.actor.startIfNeeded()
	return &reconcileHarness{
		t:        t,
		manager:  manager,
		session:  session,
		clock:    clock,
		policies: ref,
		alloc:    alloc,
	}
}

func closedChannel() <-chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

func loopbackListener(port uint16) ListenerObservation {
	return ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: port}
}

// closeReconciliation shuts the harness Manager down inside the bubble; it
// must run before the bubble test function returns.
func closeReconciliation(t *testing.T, h *reconcileHarness) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = h.manager.Close(ctx)
}

func (h *reconcileHarness) push(listeners ...ListenerObservation) {
	h.t.Helper()
	h.sequence++
	h.session.facts <- ObservationSet{
		Sequence:     h.sequence,
		Capability:   fullTestCapability,
		Observations: listeners,
		Budget:       fullObservationBudget,
	}
	// Settle the bubble: the actor consumes the set, the reconciliation
	// worker drains its wake-up signal, and every goroutine is durably
	// blocked before push returns. This replaces the former 10ms pacing
	// sleep — bubble quiescence IS the barrier, so a clock advance can
	// never race an unprocessed generation. In production the scanner's
	// two-second cadence makes this race impossible; the barrier is test
	// pacing only.
	synctest.Wait()
}

// waitFor verifies a Snapshot condition after the bubble has settled,
// allowing the reconciliation worker to act between pushes.
func (h *reconcileHarness) waitFor(describe string, cond func(Snapshot) bool) Snapshot {
	h.t.Helper()
	synctest.Wait()
	snapshot, err := h.manager.Snapshot(context.Background())
	if err != nil {
		h.t.Fatalf("Snapshot: %v", err)
	}
	if !cond(snapshot) {
		h.t.Fatalf("%s; last Snapshot: %#v", describe, snapshot)
	}
	return snapshot
}

func managedForwards(snapshot Snapshot) []ForwardSnapshot {
	if snapshot.Host == nil {
		return nil
	}
	var managed []ForwardSnapshot
	for _, forward := range snapshot.Host.Forwards {
		if forward.Kind == ForwardManaged {
			managed = append(managed, forward)
		}
	}
	return managed
}

func TestManagedForwardIdentityRoundTripsThroughSpec(t *testing.T) {
	key := remoteListenerKey{family: FamilyIPv4, scope: BindLoopback, port: 8080}
	spec := managedForwardSpec(key)
	if spec.Kind != ForwardManaged || spec.PreferredLocalPort != 8080 {
		t.Fatalf("spec = %+v, want managed kind on port 8080", spec)
	}
	recovered, known := managedForwardKey(spec.ID)
	if !known || recovered != key {
		t.Fatalf("managedForwardKey(%q) = %+v, %v; want %+v", spec.ID, recovered, known, key)
	}
	// The token is the one identity format both creation paths share.
	if string(spec.ID) != "managed:"+managedForwardToken(key) {
		t.Fatalf("ID %q does not match token %q", spec.ID, managedForwardToken(key))
	}
}

func TestAutoPolicyCreatesManagedForwardAfterTwoObservations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, []ForwardingPolicy{
			{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8080))
		// Hysteresis: the first observation alone must not create.
		snapshot := h.waitFor("first observation settles", func(s Snapshot) bool {
			return s.Host != nil && s.Host.Discovery.BaselineEstablished
		})
		if managed := managedForwards(snapshot); len(managed) != 0 {
			t.Fatalf("single observation created a Managed Forward: %+v", managed)
		}
		h.push(loopbackListener(8080))
		snapshot = h.waitFor("Managed Forward appears", func(s Snapshot) bool {
			return len(managedForwards(s)) == 1
		})
		managed := managedForwards(snapshot)
		if managed[0].RemotePort != 8080 || managed[0].AllocatedLocalPort != 8080 {
			t.Fatalf("Managed Forward = %+v, want port 8080", managed[0])
		}
	})
}

func TestAutoPolicySingleObservationDoesNotCreate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, []ForwardingPolicy{
			{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8080))
		h.waitFor("baseline settles", func(s Snapshot) bool {
			return s.Host != nil && s.Host.Discovery.BaselineEstablished
		})
		// Wall time alone must never create: the creation hysteresis counts
		// observation generations, and no new generation arrives. The clock
		// advancing an hour is irrelevant without a second observation.
		h.clock.advance(time.Hour)
		snapshot := h.currentSnapshot()
		if managed := managedForwards(snapshot); len(managed) != 0 {
			t.Fatalf("wall time alone created a Managed Forward: %+v", managed)
		}
	})
}

func (h *reconcileHarness) currentSnapshot() Snapshot {
	h.t.Helper()
	snapshot, err := h.manager.Snapshot(context.Background())
	if err != nil {
		h.t.Fatalf("Snapshot: %v", err)
	}
	return snapshot
}

func TestIgnorePolicyNeverCreates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, []ForwardingPolicy{
			{ID: "p1", Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyPort(9000)}}},
		})
		defer closeReconciliation(t, h)
		h.push(loopbackListener(9000))
		h.push(loopbackListener(9000))
		snapshot := h.waitFor("second observation settles", func(s Snapshot) bool {
			return s.Host != nil && len(s.Host.ListenerObservations) == 1 && len(s.Host.ListenerLifetimes) == 1
		})
		if managed := managedForwards(snapshot); len(managed) != 0 {
			t.Fatalf("Ignore policy created a Managed Forward: %+v", managed)
		}
	})
}

func lifetimeStatus(snapshot Snapshot, port uint16) LifetimeStatus {
	if snapshot.Host == nil {
		return ""
	}
	for _, lifetime := range snapshot.Host.ListenerLifetimes {
		if lifetime.RemotePort == port {
			return lifetime.Status
		}
	}
	return ""
}

func TestPolicyMismatchRemovalRequiresTwoObservationsAndFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, []ForwardingPolicy{
			{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8080))
		h.push(loopbackListener(8080))
		h.waitFor("Managed Forward appears", func(s Snapshot) bool { return len(managedForwards(s)) == 1 })

		// The policy changes to Ignore: removal needs two consecutive
		// observations AND five seconds, both.
		h.policies.set([]ForwardingPolicy{
			{ID: "p1", Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		h.clock.advance(4 * time.Second)
		h.push(loopbackListener(8080))
		h.push(loopbackListener(8080))
		snapshot := h.waitFor("second mismatch observation settles", func(s Snapshot) bool {
			return s.Host != nil && len(s.Host.ListenerObservations) == 1 && len(s.Host.ListenerLifetimes) == 1
		})
		if managed := managedForwards(snapshot); len(managed) != 1 {
			t.Fatalf("removed before five seconds elapsed: %+v", managed)
		}
		h.clock.advance(5 * time.Second)
		h.push(loopbackListener(8080))
		h.waitFor("Managed Forward removed after five seconds", func(s Snapshot) bool {
			return len(managedForwards(s)) == 0
		})
	})
}

func TestDisappearanceRemovesManagedForward(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, []ForwardingPolicy{
			{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8080))
		h.push(loopbackListener(8080))
		h.waitFor("Managed Forward appears", func(s Snapshot) bool { return len(managedForwards(s)) == 1 })

		h.clock.advance(4 * time.Second)
		h.push()
		h.push()
		h.waitFor("second absence settles", func(s Snapshot) bool {
			return s.Host != nil && len(s.Host.ListenerObservations) == 0
		})
		if managed := managedForwards(h.currentSnapshot()); len(managed) != 1 {
			t.Fatalf("removed before five seconds: %+v", managed)
		}
		h.clock.advance(5 * time.Second)
		h.push()
		h.waitFor("Managed Forward removed", func(s Snapshot) bool { return len(managedForwards(s)) == 0 })
	})
}

func TestOutageFreezesLifetimeGrace(t *testing.T) {
	// Decision (b) pin: a session outage freezes the tracker — outage time
	// does not count toward disappearance grace. After reconnection, the
	// first absent observation starts grace at zero.
	synctest.Test(t, func(t *testing.T) {
		sessionOne := newScriptedDiscoverySession()
		sessionTwo := newScriptedDiscoverySession()
		releaseTwo := make(chan struct{})
		connector := &sequenceConnector{
			sessions: []hostSession{sessionOne, sessionTwo},
			releases: []<-chan struct{}{closedChannel(), releaseTwo},
			started:  make(chan int, 2),
		}
		clock := newFakeClock()
		manager := newManager(managerOptions{
			host:       HostAlias("development"),
			connector:  connector,
			retryDelay: func(int) time.Duration { return 0 },
			retryWait: func(ctx context.Context, _ time.Duration) bool {
				return ctx.Err() == nil
			},
			forwardAllocator: &autoAllocator{},
			now:              clock.current,
		})
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}()
		manager.actor.startIfNeeded()

		sessionOne.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: []ListenerObservation{loopbackListener(8080)}, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if status := lifetimeStatus(snapshot, 8080); status != LifetimeNew {
			t.Fatalf("first observation status = %q, want new", status)
		}
		if snapshot.Host.Discovery.State != DiscoveryHealthy {
			t.Fatalf("discovery state = %q, want healthy", snapshot.Host.Discovery.State)
		}

		// Outage: the session ends and reconnects; no ObservationSets arrive
		// for a long wall-clock stretch.
		sessionOne.terminal <- &SessionError{Disposition: SessionRetry, Reason: SessionReasonTransport}
		close(releaseTwo)
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after reconnect: %v", err)
		}
		if got := snapshot.Host.Connection; got != ConnectionConnected {
			t.Fatalf("connection = %q, want connected", got)
		}
		clock.advance(time.Hour)
		sessionTwo.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: nil, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after outage: %v", err)
		}
		// One absent observation starts grace at one, not at ended: the outage
		// contributed nothing to the disappearance count.
		if status := lifetimeStatus(snapshot, 8080); status != LifetimeGrace {
			t.Fatalf("after outage + one absence status = %q, want grace", status)
		}
	})
}
