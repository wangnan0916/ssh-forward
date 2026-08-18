package core

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

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
	requests []ForwardSpec
	next     uint16
}

func (a *autoAllocator) Allocate(_ context.Context, spec ForwardSpec) (OwnedForward, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, spec)
	return &simpleOwnedForward{projection: ForwardSnapshot{
		ID:                 spec.ID,
		RemotePort:         spec.Remote.Port(),
		RemoteFamily:       familyForAddress(spec.Remote.Addr()),
		AllocatedLocalPort: spec.PreferredLocalPort,
		LocalFamilies:      []AddressFamily{FamilyIPv4},
	}}, nil
}

func (a *autoAllocator) allocated() []ForwardSpec {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ForwardSpec(nil), a.requests...)
}

type simpleOwnedForward struct {
	projection ForwardSnapshot
}

func (f *simpleOwnedForward) Projection() ForwardSnapshot { return cloneForward(f.projection) }
func (f *simpleOwnedForward) Close(context.Context) error { return nil }

// reconcileHarness wires a Manager with scripted discovery, a mutable
// policy source, and an auto allocator. The actor is
// armed at construction.
type reconcileHarness struct {
	t        *testing.T
	manager  *manager
	session  *scriptedDiscoverySession
	policies *policyRef
	alloc    *autoAllocator
	sequence uint64
}

func newReconcileHarness(t *testing.T, policies []ForwardingPolicy) *reconcileHarness {
	t.Helper()
	session := newScriptedDiscoverySession()
	connector := &sequenceConnector{
		sessions: []HostSession{session},
		releases: []<-chan struct{}{closedChannel()},
		started:  make(chan int, 1),
	}
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
	})
	manager.actor.startIfNeeded()
	return &reconcileHarness{
		t:        t,
		manager:  manager,
		session:  session,
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
	return append([]ForwardSnapshot(nil), snapshot.Host.Forwards...)
}

func TestManagedForwardIdentityUsesStableToken(t *testing.T) {
	key := remoteListenerKey{family: FamilyIPv4, scope: BindLoopback, port: 8080}
	spec := managedForwardSpec(key)
	if spec.PreferredLocalPort != 8080 || spec.key != key {
		t.Fatalf("spec = %+v, want port 8080 and key %+v", spec, key)
	}
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
		snapshot := h.currentSnapshot()
		if managed := managedForwards(snapshot); len(managed) != 0 {
			t.Fatalf("single observation created a Managed Forward: %+v", managed)
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
			return s.Host != nil && len(s.Host.ListenerObservations) == 1
		})
		if managed := managedForwards(snapshot); len(managed) != 0 {
			t.Fatalf("Ignore policy created a Managed Forward: %+v", managed)
		}
	})
}

func TestPolicyEditAppliesImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, nil)
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8080))
		h.push(loopbackListener(8080))
		h.waitFor("baseline with no policy", func(s Snapshot) bool {
			return s.Host != nil && s.Host.Discovery.BaselineEstablished && len(managedForwards(s)) == 0
		})
		h.policies.set([]ForwardingPolicy{
			{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		h.manager.reconciler.notifyPolicy()
		h.waitFor("Remembered Auto-forward applies without another observation", func(s Snapshot) bool {
			return len(managedForwards(s)) == 1
		})
	})
}

func TestPolicyEditToIgnoreRemovesImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, []ForwardingPolicy{
			{ID: "p1", Action: PolicyAutoForward, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8080))
		h.push(loopbackListener(8080))
		h.waitFor("Managed Forward appears", func(s Snapshot) bool { return len(managedForwards(s)) == 1 })

		h.policies.set([]ForwardingPolicy{
			{ID: "p1", Action: PolicyIgnore, Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}}},
		})
		h.manager.reconciler.notifyPolicy()
		h.waitFor("Ignore removes the Managed Forward without another observation", func(s Snapshot) bool {
			return len(managedForwards(s)) == 0
		})
	})
}

func TestLocalPortConflictAppearsOnSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		connector := &sequenceConnector{
			sessions: []HostSession{session},
			releases: []<-chan struct{}{closedChannel()},
			started:  make(chan int, 1),
		}
		manager := newManager(managerOptions{
			host:             HostAlias("development"),
			connector:        connector,
			retryDelay:       func(int) time.Duration { return 0 },
			retryWait:        func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
			forwardAllocator: conflictAllocator{},
			policies: func() []ForwardingPolicy {
				return []ForwardingPolicy{{
					ID: "p1", Action: PolicyAutoForward,
					Conditions: []PolicyCondition{{RemotePorts: policyPort(8080)}},
				}}
			},
		})
		h := &reconcileHarness{t: t, manager: manager, session: session}
		defer closeReconciliation(t, h)
		manager.actor.startIfNeeded()
		h.push(loopbackListener(8080))
		h.push(loopbackListener(8080))
		snapshot := h.waitFor("Local Port Conflict is on the Snapshot", func(s Snapshot) bool {
			return s.Host != nil && len(s.Host.LocalPortConflicts) == 1
		})
		conflict := snapshot.Host.LocalPortConflicts[0]
		if conflict.RemotePort != 8080 || conflict.RemoteFamily != FamilyIPv4 || conflict.BindScope != BindLoopback {
			t.Fatalf("conflict = %+v", conflict)
		}
		if len(managedForwards(snapshot)) != 0 {
			t.Fatalf("conflict still created a forward: %+v", managedForwards(snapshot))
		}
	})
}

type conflictAllocator struct{}

func (conflictAllocator) Allocate(context.Context, ForwardSpec) (OwnedForward, error) {
	return nil, &DomainError{Kind: ErrorLocalPortConflict, Retryable: true}
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

		h.push()
		snapshot := h.waitFor("first absence settles", func(s Snapshot) bool {
			return s.Host != nil && len(s.Host.ListenerObservations) == 0
		})
		if managed := managedForwards(snapshot); len(managed) != 1 {
			t.Fatalf("removed after one absence: %+v", managed)
		}
		h.push()
		h.waitFor("Managed Forward removed", func(s Snapshot) bool { return len(managedForwards(s)) == 0 })
	})
}
