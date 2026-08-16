package core

import (
	"context"
	"sync"
	"testing"
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
// armed at construction (in the real product the first command does that).
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
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
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

func (h *reconcileHarness) push(listeners ...ListenerObservation) {
	h.t.Helper()
	h.sequence++
	h.session.facts <- ObservationSet{
		Sequence:     h.sequence,
		Capability:   fullTestCapability,
		Observations: listeners,
		Budget:       fullObservationBudget,
	}
	// Settle the actor and the reconciliation worker before the test
	// advances the injected clock: the five-second removal floor is
	// measured from the observation's processing, so a clock advance must
	// not race an unprocessed generation. In production the scanner's
	// two-second cadence makes this race impossible; the barrier is test
	// pacing only.
	time.Sleep(10 * time.Millisecond)
}

// waitFor verifies a Snapshot condition through the Manager interface,
// allowing the reconciliation worker to act between pushes.
func (h *reconcileHarness) waitFor(describe string, cond func(Snapshot) bool) Snapshot {
	h.t.Helper()
	return waitForSnapshot(h.t, h.manager, describe, cond)
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

func askPorts(snapshot Snapshot) map[uint16]bool {
	ports := make(map[uint16]bool)
	if snapshot.Host == nil {
		return ports
	}
	for _, listener := range snapshot.Host.AskListeners {
		ports[listener.RemotePort] = true
	}
	return ports
}

func TestAutoPolicyCreatesManagedForwardAfterTwoObservations(t *testing.T) {
	h := newReconcileHarness(t, []ForwardingPolicy{
		{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	})
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
}

func TestAutoPolicySingleObservationDoesNotCreate(t *testing.T) {
	h := newReconcileHarness(t, []ForwardingPolicy{
		{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	})
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
}

func (h *reconcileHarness) currentSnapshot() Snapshot {
	h.t.Helper()
	snapshot, err := h.manager.Snapshot(context.Background())
	if err != nil {
		h.t.Fatalf("Snapshot: %v", err)
	}
	return snapshot
}

func TestIgnorePolicyNeverCreatesAndNeverAsks(t *testing.T) {
	h := newReconcileHarness(t, []ForwardingPolicy{
		{ID: "p1", Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyPort(9000)}}},
	})
	h.push(loopbackListener(9000))
	h.push(loopbackListener(9000))
	snapshot := h.waitFor("second observation settles", func(s Snapshot) bool {
		return s.Host != nil && len(s.Host.ListenerObservations) == 1 && len(s.Host.ListenerLifetimes) == 1
	})
	if managed := managedForwards(snapshot); len(managed) != 0 {
		t.Fatalf("Ignore policy created a Managed Forward: %+v", managed)
	}
	if ports := askPorts(snapshot); ports[9000] {
		t.Fatalf("Ignore policy left port 9000 in Ask: %+v", ports)
	}
}

func TestDefaultAskAppearsPostBaseline(t *testing.T) {
	h := newReconcileHarness(t, nil)
	// The baseline generation's own Listeners are pre-baseline and never
	// Ask; the first listener observed after the baseline enters Ask.
	h.push(loopbackListener(8080))
	h.waitFor("baseline settles", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Discovery.BaselineEstablished
	})
	h.push(loopbackListener(8081))
	snapshot := h.waitFor("Ask appears", func(s Snapshot) bool {
		return askPorts(s)[8081]
	})
	if ports := askPorts(snapshot); ports[8080] {
		t.Fatalf("pre-baseline listener entered Ask: %+v", ports)
	}
	if len(snapshot.Host.AskListeners) != 1 {
		t.Fatalf("AskListeners = %+v, want exactly port 8081", snapshot.Host.AskListeners)
	}
}

func TestPreBaselineListenerNeverAsks(t *testing.T) {
	h := newReconcileHarness(t, nil)
	h.push(loopbackListener(8080))
	h.push(loopbackListener(8080))
	snapshot := h.waitFor("listener settles post-baseline", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Discovery.BaselineEstablished && len(s.Host.ListenerLifetimes) == 1
	})
	if ports := askPorts(snapshot); ports[8080] {
		t.Fatalf("pre-baseline listener entered Ask: %+v", ports)
	}
	// A listener first observed after the baseline does ask.
	h.push(loopbackListener(8081))
	snapshot = h.waitFor("new listener asks", func(s Snapshot) bool {
		return askPorts(s)[8081]
	})
	if ports := askPorts(snapshot); ports[8080] {
		t.Fatalf("pre-baseline listener still asks: %+v", ports)
	}
}

func TestApprovalCreatesImmediatelyAndRetiresOnEnded(t *testing.T) {
	h := newReconcileHarness(t, nil)
	h.push(loopbackListener(8000)) // baseline generation, pre-baseline
	h.waitFor("baseline settles", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Discovery.BaselineEstablished
	})
	h.push(loopbackListener(8080))
	h.waitFor("Ask appears", func(s Snapshot) bool { return askPorts(s)[8080] })

	outcome, err := h.manager.Execute(context.Background(), ApproveListener{
		CommandID:  CommandID("approve-1"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if outcome.Kind != OutcomeApprovalRecorded {
		t.Fatalf("approve outcome = %q, want approval_recorded", outcome.Kind)
	}
	snapshot := h.waitFor("Managed Forward appears", func(s Snapshot) bool {
		return len(managedForwards(s)) == 1
	})
	managed := managedForwards(snapshot)
	if managed[0].RemotePort != 8080 {
		t.Fatalf("approved Managed Forward = %+v, want port 8080", managed[0])
	}
	if ports := askPorts(snapshot); ports[8080] {
		t.Fatalf("approved listener still asks: %+v", ports)
	}

	// The listener disappears for graceCycles+1 observations; the ended
	// verdict retires the One-time Approval and removes the forward.
	h.push()
	h.push()
	h.push()
	h.waitFor("lifetime enters grace", func(s Snapshot) bool {
		return lifetimeStatus(s, 8080) == LifetimeGrace
	})
	h.push()
	h.waitFor("lifetime ends and forward is removed", func(s Snapshot) bool {
		return lifetimeStatus(s, 8080) == LifetimeEnded && len(managedForwards(s)) == 0
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

func TestSuppressionHidesAskAndRetiresWithLifetime(t *testing.T) {
	h := newReconcileHarness(t, nil)
	h.push(loopbackListener(8000)) // baseline generation, pre-baseline
	h.waitFor("baseline settles", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Discovery.BaselineEstablished
	})
	h.push(loopbackListener(8080))
	h.waitFor("Ask appears", func(s Snapshot) bool { return askPorts(s)[8080] })

	if _, err := h.manager.Execute(context.Background(), SuppressListener{
		CommandID:  CommandID("suppress-1"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
	}); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	h.push(loopbackListener(8080))
	h.waitFor("suppressed listener no longer asks", func(s Snapshot) bool {
		return s.Host != nil && !askPorts(s)[8080]
	})

	// The suppression lasts one Listener Lifetime: after the listener ends,
	// a new lifetime on the same port asks again.
	h.push()
	h.push()
	h.push()
	h.push()
	h.waitFor("lifetime ends", func(s Snapshot) bool { return lifetimeStatus(s, 8080) == LifetimeEnded })
	h.push(loopbackListener(8080))
	h.waitFor("new lifetime asks again", func(s Snapshot) bool {
		return askPorts(s)[8080]
	})
}

func TestPolicyEditChangingOnlyAskVerdictsRefreshesAsk(t *testing.T) {
	h := newReconcileHarness(t, nil)
	h.push(loopbackListener(8000)) // baseline generation, pre-baseline
	h.waitFor("baseline settles", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Discovery.BaselineEstablished
	})
	h.push(loopbackListener(8080))
	h.waitFor("Ask appears", func(s Snapshot) bool { return askPorts(s)[8080] })

	// The edit changes only the Ask verdict: no policy touches any
	// forward, so the generation's delta is empty — but the Ask list must
	// still follow the file (the policy cache commits before the delta
	// check, and a changed set republishes even without a delta).
	h.policies.set([]ForwardingPolicy{
		{ID: "p1", Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	})
	h.push(loopbackListener(8080))
	h.waitFor("Ask list follows the edited policy", func(s Snapshot) bool {
		return !askPorts(s)[8080]
	})

	// And the reverse: an edit back to default Ask restores the Ask entry.
	h.policies.set(nil)
	h.push(loopbackListener(8080))
	h.waitFor("Ask list follows the reverted policy", func(s Snapshot) bool {
		return askPorts(s)[8080]
	})
}

func TestPolicyMismatchRemovalRequiresTwoObservationsAndFiveSeconds(t *testing.T) {
	h := newReconcileHarness(t, []ForwardingPolicy{
		{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	})
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
}

func TestDisappearanceRemovesManagedForward(t *testing.T) {
	h := newReconcileHarness(t, []ForwardingPolicy{
		{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	})
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
}

func TestManualForwardNotReconciled(t *testing.T) {
	h := newReconcileHarness(t, []ForwardingPolicy{
		{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
	})
	if _, err := h.manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("manual-1"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	// The listener disappears entirely; policy would remove a Managed
	// Forward, but the Manual Forward must survive reconciliation.
	h.push(loopbackListener(8080))
	h.push(loopbackListener(8080))
	h.waitFor("observations settle", func(s Snapshot) bool {
		return s.Host != nil && len(s.Host.ListenerLifetimes) == 1
	})
	h.clock.advance(time.Hour)
	for range 4 {
		h.push()
	}
	h.waitFor("listener ends", func(s Snapshot) bool { return lifetimeStatus(s, 8080) == LifetimeEnded })
	snapshot := h.currentSnapshot()
	found := false
	for _, forward := range snapshot.Host.Forwards {
		if forward.Kind == ForwardManual && forward.RemotePort == 8080 {
			found = true
		}
	}
	if !found {
		t.Fatalf("Manual Forward was reconciled away: %+v", snapshot.Host.Forwards)
	}
}

func TestOutageFreezesLifetimeGrace(t *testing.T) {
	// Decision (b) pin: a session outage freezes the tracker — outage time
	// does not count toward disappearance grace. After reconnection, the
	// first absent observation starts grace at zero.
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
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	manager.actor.startIfNeeded()

	sessionOne.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: []ListenerObservation{loopbackListener(8080)}, Budget: fullObservationBudget}
	snapshot := waitForSnapshot(t, manager, "first observation settles", func(s Snapshot) bool {
		return s.Host != nil && lifetimeStatus(s, 8080) == LifetimeNew
	})
	if snapshot.Host.Discovery.State != DiscoveryHealthy {
		t.Fatalf("discovery state = %q, want healthy", snapshot.Host.Discovery.State)
	}

	// Outage: the session ends and reconnects; no ObservationSets arrive
	// for a long wall-clock stretch.
	sessionOne.terminal <- &SessionError{Disposition: SessionRetry, Reason: SessionReasonTransport}
	close(releaseTwo)
	waitForSnapshot(t, manager, "reconnected", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Connection == ConnectionConnected
	})
	clock.advance(time.Hour)
	sessionTwo.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: nil, Budget: fullObservationBudget}
	snapshot = waitForSnapshot(t, manager, "absent observation after outage", func(s Snapshot) bool {
		return s.Host != nil && s.Host.Discovery.BaselineEstablished && len(s.Host.ListenerObservations) == 0
	})
	// One absent observation starts grace at one, not at ended: the outage
	// contributed nothing to the disappearance count.
	if status := lifetimeStatus(snapshot, 8080); status != LifetimeGrace {
		t.Fatalf("after outage + one absence status = %q, want grace", status)
	}
}
