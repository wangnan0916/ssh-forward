package core

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"time"
)

type wakeKind int

const (
	wakeObservation wakeKind = iota
	wakePolicy
)

// reconciler owns Managed Forward lifecycle state: the policy cache, create
// and removal hysteresis, Local Port Conflicts, and the worker's wake-up
// signals. Like forwardTable, it holds no lock of its own — the Manager lock
// guards it, except the policy source which the worker invokes before taking
// any lock. The worker goroutine is the only writer of hysteresis and conflicts.
type reconciler struct {
	policyCache      []ForwardingPolicy
	policyDiagnostic string
	policySource     func() ([]ForwardingPolicy, string)
	createState      map[remoteListenerKey]int
	removalState     map[ForwardID]int
	conflicts        map[remoteListenerKey]LocalPortConflict
	observe          chan struct{}
	policy           chan struct{}
}

func newReconciler(policySource func() ([]ForwardingPolicy, string)) *reconciler {
	policies, diagnostic := policySource()
	return &reconciler{
		policyCache:      policies,
		policyDiagnostic: diagnostic,
		policySource:     policySource,
		createState:      make(map[remoteListenerKey]int),
		removalState:     make(map[ForwardID]int),
		conflicts:        make(map[remoteListenerKey]LocalPortConflict),
		observe:          make(chan struct{}, 8),
		policy:           make(chan struct{}, 8),
	}
}

func (r *reconciler) notify() {
	select {
	case r.observe <- struct{}{}:
	default:
	}
}

func (r *reconciler) notifyPolicy() {
	select {
	case r.policy <- struct{}{}:
	default:
	}
}

func (r *reconciler) commit(policies []ForwardingPolicy, diagnostic string) {
	r.policyCache = policies
	r.policyDiagnostic = diagnostic
}

func policySetsEqual(left, right []ForwardingPolicy) bool {
	return slices.EqualFunc(left, right, func(a, b ForwardingPolicy) bool {
		return reflect.DeepEqual(a, b)
	})
}

func (r *reconciler) createReady(key remoteListenerKey, hasManaged bool) bool {
	if hasManaged {
		delete(r.createState, key)
		return false
	}
	r.createState[key]++
	if r.createState[key] < 2 {
		return false
	}
	delete(r.createState, key)
	return true
}

func (r *reconciler) removalStep(id ForwardID, desired bool) bool {
	if desired {
		delete(r.removalState, id)
		return false
	}
	r.removalState[id]++
	if r.removalState[id] < 2 {
		return false
	}
	delete(r.removalState, id)
	return true
}

func (r *reconciler) resetHysteresis() {
	clear(r.createState)
	clear(r.removalState)
}

func (r *reconciler) recordConflict(key remoteListenerKey) {
	r.conflicts[key] = LocalPortConflict{
		RemotePort:   key.port,
		RemoteFamily: key.family,
		BindScope:    key.scope,
	}
}

func (r *reconciler) clearConflict(key remoteListenerKey) {
	delete(r.conflicts, key)
}

func (r *reconciler) conflictSnapshots() []LocalPortConflict {
	if len(r.conflicts) == 0 {
		return nil
	}
	out := make([]LocalPortConflict, 0, len(r.conflicts))
	for _, conflict := range r.conflicts {
		out = append(out, conflict)
	}
	slices.SortFunc(out, func(left, right LocalPortConflict) int {
		if order := cmp.Compare(left.RemoteFamily, right.RemoteFamily); order != 0 {
			return order
		}
		if order := cmp.Compare(left.BindScope, right.BindScope); order != 0 {
			return order
		}
		return cmp.Compare(left.RemotePort, right.RemotePort)
	})
	return out
}

func (m *manager) reconcileLoop() {
	defer m.workers.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.reconciler.observe:
			drainWake(m.reconciler.observe)
			m.reconcileOnce(wakeObservation)
		case <-m.reconciler.policy:
			drainWake(m.reconciler.policy)
			m.reconcileOnce(wakePolicy)
		}
	}
}

func (m *manager) policyPollLoop(interval time.Duration) {
	defer m.workers.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reconciler.notifyPolicy()
		}
	}
}

func drainWake(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (m *manager) reconcileOnce(kind wakeKind) {
	policies, diagnostic := m.reconciler.policySource()
	policyChanged := !policySetsEqual(m.reconciler.policyCache, policies)
	diagnosticChanged := diagnostic != m.reconciler.policyDiagnostic
	if kind == wakePolicy && !policyChanged {
		if !diagnosticChanged {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed {
			return
		}
		m.reconciler.commit(policies, diagnostic)
		m.publishLocked()
		return
	}

	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	host := m.view
	managedEntries := m.forwards.managedForwardsLocked()
	m.mu.RUnlock()

	toCreate, toRemove := m.reconciler.decide(host.ListenerObservations, managedEntries, policies, policyChanged)

	if len(toCreate) == 0 && len(toRemove) == 0 {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed {
			return
		}
		m.reconciler.commit(policies, diagnostic)
		if policyChanged {
			m.reconciler.resetHysteresis()
			m.publishLocked()
		} else if diagnosticChanged {
			m.publishLocked()
		}
		return
	}

	type createdForward struct {
		owner OwnedForward
		key   remoteListenerKey
	}
	var created []createdForward
	failed := make(map[remoteListenerKey]struct{})
	for _, spec := range toCreate {
		owner, err := m.forwardAllocator.Allocate(context.Background(), spec)
		if err != nil {
			var domain *DomainError
			if errors.As(err, &domain) && domain.Kind == ErrorLocalPortConflict {
				failed[spec.key] = struct{}{}
			}
			continue
		}
		created = append(created, createdForward{owner: owner, key: spec.key})
	}

	// Map mutations stay under the Manager lock; Allocate already ran
	// outside it and Close runs after Unlock.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		for _, item := range created {
			_ = item.owner.Close(context.Background())
		}
		return
	}
	m.reconciler.commit(policies, diagnostic)
	if policyChanged {
		m.reconciler.resetHysteresis()
	}
	changed := policyChanged || diagnosticChanged
	for key := range failed {
		m.reconciler.recordConflict(key)
		changed = true
	}
	var extraClose []OwnedForward
	for _, item := range created {
		if !m.forwards.add(item.owner, item.key) {
			extraClose = append(extraClose, item.owner)
			continue
		}
		m.reconciler.clearConflict(item.key)
		changed = true
	}
	for _, id := range toRemove {
		owner, key, found := m.forwards.removeDirect(id)
		if !found {
			continue
		}
		extraClose = append(extraClose, owner)
		delete(m.reconciler.removalState, id)
		m.reconciler.clearConflict(key)
		changed = true
	}
	if changed {
		m.publishLocked()
	}
	m.mu.Unlock()
	for _, owner := range extraClose {
		_ = owner.Close(context.Background())
	}
}

// decide is the Managed Forward intent: observations + policies + hysteresis
// become create/remove lists. Allocate, Close, and the Forward table stay
// in reconcileOnce with the lock policy.
func (r *reconciler) decide(observations []ListenerObservation, managed []managedForwardEntry, policies []ForwardingPolicy, policyChanged bool) (toCreate []ForwardSpec, toRemove []ForwardID) {
	ordered := sortPolicies(policies)
	desired := make(map[remoteListenerKey]struct{})
	for _, observation := range observations {
		if evaluateOrdered(ordered, observation).Action == PolicyAutoForward {
			desired[listenerKey(observation)] = struct{}{}
		}
	}
	managedKeys := make(map[remoteListenerKey]struct{}, len(managed))
	for _, entry := range managed {
		managedKeys[entry.key] = struct{}{}
	}
	if policyChanged {
		for key := range desired {
			if _, already := managedKeys[key]; !already {
				toCreate = append(toCreate, managedForwardSpec(key))
			}
		}
		for _, entry := range managed {
			if _, stillDesired := desired[entry.key]; !stillDesired {
				toRemove = append(toRemove, entry.id)
			}
		}
		return toCreate, toRemove
	}
	for key := range desired {
		_, managed := managedKeys[key]
		if r.createReady(key, managed) {
			toCreate = append(toCreate, managedForwardSpec(key))
		}
	}
	for _, entry := range managed {
		_, stillDesired := desired[entry.key]
		if r.removalStep(entry.id, stillDesired) {
			toRemove = append(toRemove, entry.id)
		}
	}
	return toCreate, toRemove
}

func managedForwardSpec(key remoteListenerKey) ForwardSpec {
	return ForwardSpec{
		ID:                 ForwardID("managed:" + managedForwardToken(key)),
		Remote:             loopbackTarget(key.family, key.port),
		PreferredLocalPort: key.port,
		key:                key,
	}
}

func managedForwardToken(key remoteListenerKey) string {
	return string(key.family) + ":" + string(key.scope) + ":" + strconv.Itoa(int(key.port))
}
