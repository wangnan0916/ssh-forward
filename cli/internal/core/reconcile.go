package core

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// reconciler owns the Forwarding Policy subsystem's state: the policy cache
// and source, the creation and removal hysteresis, and the worker's wake-up
// signal. Like forwardTable, it holds no lock of its own — the Manager lock
// guards it, and its methods are called only while that lock is held, with
// one exception: the policy source is a user-injected function (file-backed
// in production) and the reconciliation worker invokes it before taking any
// lock. The worker goroutine is the only writer of the hysteresis state.
type reconciler struct {
	policyCache  []ForwardingPolicy
	evaluated    []ForwardingPolicy
	policySource func() []ForwardingPolicy
	createState  map[remoteListenerKey]int
	removalState map[ForwardID]int
	reconcile    chan struct{}
}

func newReconciler(policySource func() []ForwardingPolicy) *reconciler {
	initial := policySource()
	return &reconciler{
		policyCache:  initial,
		evaluated:    sortPolicies(initial),
		policySource: policySource,
		createState:  make(map[remoteListenerKey]int),
		removalState: make(map[ForwardID]int),
		reconcile:    make(chan struct{}, 8),
	}
}

// notify wakes the reconciliation worker after the actor applied a new
// observation generation. The channel is buffered because the signal must
// not be lost while the worker is busy with a previous generation.
func (r *reconciler) notify() {
	select {
	case r.reconcile <- struct{}{}:
	default:
	}
}

// commitPolicies records the policy set this generation evaluated (the
// source order, for equality against the next read) and its priority-sorted
// evaluation order (sorted once per generation), reporting whether the set
// differs from the cache so the worker can republish when an edit changed
// only which listeners match.
func (r *reconciler) commitPolicies(source, ordered []ForwardingPolicy) bool {
	changed := !policySetsEqual(r.policyCache, source)
	r.policyCache = source
	r.evaluated = ordered
	return changed
}

// policySetsEqual compares policy sets by value, treating nil and empty as
// equal: a source that alternates between nil and empty must not count as
// an edit.
func policySetsEqual(left, right []ForwardingPolicy) bool {
	return slices.EqualFunc(left, right, func(a, b ForwardingPolicy) bool {
		return reflect.DeepEqual(a, b)
	})
}

// createReady advances the creation hysteresis: a Managed Forward needs two
// consecutive observations of the same auto verdict. It reports whether
// this observation makes the creation due. hasManaged resets the count: an
// existing forward (or one registered meanwhile) ends the accumulation.
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

// removalStep advances the removal hysteresis for one Managed Forward that
// is no longer desired, reporting whether the removal is due now. desired
// keeps the forward and clears the count; otherwise two consecutive
// observations make the removal due.
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

// reconcileLoop is the Manager's single reconciliation worker. The actor
// signals it once per applied observation generation (reconciler.notify);
// the worker reads the mirror outside the Manager lock, evaluates policies,
// and executes the Managed Forward delta — so allocation and teardown never
// block the actor. It is the writer of the Managed Forward lifecycle and
// registers under the Manager lock. The drain
// loop consumes every buffered notification so each generation advances
// the hysteresis exactly once, even when the actor outruns the worker.
func (m *manager) reconcileLoop() {
	defer m.workers.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.reconciler.reconcile:
			for {
				m.reconcileOnce()
				select {
				case <-m.reconciler.reconcile:
				default:
					goto settled
				}
			}
		settled:
		}
	}
}

func (m *manager) reconcileOnce() {
	policies := m.reconciler.policySource()
	ordered := sortPolicies(policies)
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	host := m.hostSnapshot
	managedEntries := m.forwards.managedForwardsLocked()
	m.mu.RUnlock()

	observationByKey := make(map[remoteListenerKey]ListenerObservation, len(host.ListenerObservations))
	for _, observation := range host.ListenerObservations {
		observationByKey[listenerKey(observation)] = observation
	}

	desired := make(map[remoteListenerKey]struct{})
	for key, observation := range observationByKey {
		if evaluateOrdered(ordered, observation).Action == PolicyAutoForward {
			desired[key] = struct{}{}
		}
	}

	managedKeys := make(map[remoteListenerKey]struct{}, len(managedEntries))
	for _, entry := range managedEntries {
		managedKeys[entry.key] = struct{}{}
	}

	var toCreate []forwardSpec
	for key := range desired {
		_, managed := managedKeys[key]
		if !m.reconciler.createReady(key, managed) {
			continue
		}
		toCreate = append(toCreate, managedForwardSpec(key))
	}

	var toRemove []ForwardID
	for _, entry := range managedEntries {
		_, stillDesired := desired[entry.key]
		if m.reconciler.removalStep(entry.id, stillDesired) {
			toRemove = append(toRemove, entry.id)
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	policyChanged := m.reconciler.commitPolicies(policies, ordered)
	if len(toCreate) == 0 && len(toRemove) == 0 {
		if policyChanged {
			m.publishLocked()
		}
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	var created []ownedForward
	for _, spec := range toCreate {
		owner, err := m.forwardAllocator.Allocate(context.Background(), spec)
		if err != nil {
			continue
		}
		created = append(created, owner)
	}
	type removedForward struct {
		id    ForwardID
		owner ownedForward
	}
	var removed []removedForward
	for _, id := range toRemove {
		owner, found := m.forwards.removeDirect(id)
		if found {
			removed = append(removed, removedForward{id: id, owner: owner})
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		for _, owner := range created {
			_ = owner.Close(context.Background())
		}
		return
	}
	for _, owner := range created {
		if !m.forwards.add(owner) {
			_ = owner.Close(context.Background())
		}
	}
	for _, forward := range removed {
		_ = forward.owner.Close(context.Background())
		delete(m.reconciler.removalState, forward.id)
	}
	if len(created) != 0 || len(removed) != 0 {
		m.publishLocked()
	}
}

func managedForwardSpec(key remoteListenerKey) forwardSpec {
	return forwardSpec{
		ID:                 ForwardID("managed:" + managedForwardToken(key)),
		Remote:             loopbackTarget(key.family, key.port),
		PreferredLocalPort: key.port,
	}
}

// managedForwardToken builds a stable, collision-resistant Managed Forward
// identity from the listener key: reconciliation must address the same
// forward across observations.
func managedForwardToken(key remoteListenerKey) string {
	return string(key.family) + ":" + string(key.scope) + ":" + strconv.Itoa(int(key.port))
}

// managedForwardKey recovers the listener key a Managed Forward serves from
// its identity (managed:<family>:<scope>:<port>). Unknown shapes report false.
func managedForwardKey(id ForwardID) (remoteListenerKey, bool) {
	token, found := strings.CutPrefix(string(id), "managed:")
	if !found {
		return remoteListenerKey{}, false
	}
	family, rest, found := strings.Cut(token, ":")
	if !found {
		return remoteListenerKey{}, false
	}
	scope, portText, found := strings.Cut(rest, ":")
	if !found {
		return remoteListenerKey{}, false
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return remoteListenerKey{}, false
	}
	return remoteListenerKey{
		family: AddressFamily(family),
		scope:  ListenerBindScope(scope),
		port:   uint16(port),
	}, true
}
