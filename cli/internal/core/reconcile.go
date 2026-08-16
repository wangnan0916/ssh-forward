package core

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// managedRemoval tracks the removal hysteresis for one Managed Forward: it
// is removed after two consecutive observations without it AND five seconds
// of wall time (decision recorded in implementation-sequence.md slice 5).
// The five-second floor is the only place wall time reaches the core; the
// observation count comes from the actor's fact stream.
type managedRemoval struct {
	absentObservations int
	absentSince        time.Time
}

// reconciler owns the Forwarding Policy subsystem's state (slice 5): the
// policy cache and source, the One-time decision sets, the creation and
// removal hysteresis, the worker's wake-up signal, and the Ask derivation.
// Like forwardTable, it holds no lock of its own — the Manager lock guards
// it, and its methods are called only while that lock is held, with one
// exception: the policy source is a user-injected function (file-backed in
// production) and the reconciliation worker invokes it before taking any
// lock. The worker goroutine is the only writer of the hysteresis state;
// commands write the decision sets through the Manager.
type reconciler struct {
	policyCache  []ForwardingPolicy
	policySource func() []ForwardingPolicy
	now          func() time.Time
	approvals    map[remoteListenerKey]struct{}
	suppressions map[remoteListenerKey]struct{}
	createState  map[remoteListenerKey]int
	removalState map[ForwardID]*managedRemoval
	reconcile    chan struct{}
}

func newReconciler(policySource func() []ForwardingPolicy, now func() time.Time) *reconciler {
	return &reconciler{
		policyCache:  policySource(),
		policySource: policySource,
		now:          now,
		approvals:    make(map[remoteListenerKey]struct{}),
		suppressions: make(map[remoteListenerKey]struct{}),
		createState:  make(map[remoteListenerKey]int),
		removalState: make(map[ForwardID]*managedRemoval),
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

// cloneDecisions snapshots the One-time decision sets so the worker can
// evaluate a generation without holding the Manager lock.
func (r *reconciler) cloneDecisions() (approvals, suppressions map[remoteListenerKey]struct{}) {
	approvals = make(map[remoteListenerKey]struct{}, len(r.approvals))
	for key := range r.approvals {
		approvals[key] = struct{}{}
	}
	suppressions = make(map[remoteListenerKey]struct{}, len(r.suppressions))
	for key := range r.suppressions {
		suppressions[key] = struct{}{}
	}
	return approvals, suppressions
}

// retiredDecisions finds One-time decisions whose Listener Lifetime ended
// or was replaced: "Listener ended" retires One-time Approvals, and a new
// Lifetime must not inherit a Suppression.
func (r *reconciler) retiredDecisions(statusByKey map[remoteListenerKey]LifetimeStatus) []remoteListenerKey {
	var retired []remoteListenerKey
	for key := range r.approvals {
		switch statusByKey[key] {
		case LifetimeEnded, LifetimeReplaced:
			retired = append(retired, key)
		}
	}
	for key := range r.suppressions {
		switch statusByKey[key] {
		case LifetimeEnded, LifetimeReplaced:
			retired = append(retired, key)
		}
	}
	return retired
}

// retire removes retired One-time decisions and their creation state.
func (r *reconciler) retire(retired []remoteListenerKey) {
	for _, key := range retired {
		delete(r.approvals, key)
		delete(r.suppressions, key)
		delete(r.createState, key)
	}
}

// commitPolicies records the policy set this generation evaluated and
// reports whether it differs from the cached set, so the worker can
// republish when an edit changed only derived Ask verdicts.
func (r *reconciler) commitPolicies(policies []ForwardingPolicy) bool {
	changed := !policySetsEqual(r.policyCache, policies)
	r.policyCache = policies
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
// keeps the forward and clears the count; a retired One-time Approval
// removes its forward immediately (the approval's credential was scoped to
// the ended Lifetime); otherwise two consecutive observations AND five
// seconds make the removal due.
func (r *reconciler) removalStep(id ForwardID, desired, retired bool) bool {
	if desired {
		delete(r.removalState, id)
		return false
	}
	if retired {
		return true
	}
	record := r.removalState[id]
	if record == nil {
		record = &managedRemoval{}
		r.removalState[id] = record
	}
	record.absentObservations++
	if record.absentObservations == 1 {
		record.absentSince = r.now()
	}
	return record.absentObservations >= 2 && !record.absentSince.Add(5*time.Second).After(r.now())
}

// askListeners derives the Ask list from the current mirror: Listeners
// first observed after the Discovery Baseline, not suppressed, and whose
// policy evaluation yields Ask (no policy matched, or the matched policy
// could not act automatically). It is derived state, not stored state, so
// it can never drift from the observation generation it describes.
func (r *reconciler) askListeners(host HostSnapshot) []ListenerAskSnapshot {
	if !host.Discovery.BaselineEstablished {
		return nil
	}
	postBaseline := make(map[remoteListenerKey]bool, len(host.ListenerLifetimes))
	for _, verdict := range host.ListenerLifetimes {
		postBaseline[lifetimeKey(verdict)] = verdict.PostBaseline
	}
	ask := make([]ListenerAskSnapshot, 0)
	for _, observation := range host.ListenerObservations {
		key := listenerKey(observation)
		if !postBaseline[key] {
			continue
		}
		if _, suppressed := r.suppressions[key]; suppressed {
			continue
		}
		if _, approved := r.approvals[key]; approved {
			continue
		}
		if evaluatePolicies(r.policyCache, observation).Action != PolicyAsk {
			continue
		}
		ask = append(ask, ListenerAskSnapshot{
			Family:     observation.Family,
			BindScope:  observation.BindScope,
			RemotePort: observation.RemotePort,
		})
	}
	return ask
}

// reconcileLoop is the Manager's single reconciliation worker. The actor
// signals it once per applied observation generation (reconciler.notify);
// the worker reads the mirror outside the Manager lock, evaluates policies,
// and executes the Managed Forward delta — so allocation and teardown never
// block the actor or a command. It is the primary writer of the Managed
// Forward lifecycle; the approve command creates through the same
// allocation path, and both register under the Manager lock. The drain
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
	// The policy source is a user-injected function (file-backed in
	// production): call it outside both locks.
	policies := m.reconciler.policySource()
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	host := m.hostSnapshot
	approvals, _ := m.reconciler.cloneDecisions()
	managedKeys := m.forwards.managedKeysLocked()
	m.mu.RUnlock()

	observationByKey := make(map[remoteListenerKey]ListenerObservation, len(host.ListenerObservations))
	for _, observation := range host.ListenerObservations {
		observationByKey[listenerKey(observation)] = observation
	}
	statusByKey := make(map[remoteListenerKey]LifetimeStatus, len(host.ListenerLifetimes))
	for _, verdict := range host.ListenerLifetimes {
		statusByKey[lifetimeKey(verdict)] = verdict.Status
	}

	// Desired Managed Forwards: one per Listener that is either governed by
	// an active One-time Approval or matched by an auto_forward policy.
	desired := make(map[remoteListenerKey]struct{})
	for key, observation := range observationByKey {
		if _, approved := approvals[key]; approved {
			desired[key] = struct{}{}
			continue
		}
		if evaluatePolicies(policies, observation).Action == PolicyAutoForward {
			desired[key] = struct{}{}
		}
	}

	// Retire one-time decisions whose Listener Lifetime ended or was
	// replaced: "Listener ended" retires One-time Approvals, and a new
	// Lifetime must not inherit a Suppression.
	retired := m.reconciler.retiredDecisions(statusByKey)
	retiredKeys := make(map[remoteListenerKey]struct{}, len(retired))
	for _, key := range retired {
		retiredKeys[key] = struct{}{}
	}

	// Advance the creation hysteresis: a Managed Forward needs two
	// consecutive observations of the same auto verdict. The has-managed
	// check reads the lock-snapshot taken above, so it never races a
	// command's add.
	var toCreate []forwardSpec
	for key := range desired {
		_, managed := managedKeys[key]
		if !m.reconciler.createReady(key, managed) {
			continue
		}
		spec, err := managedForwardSpec(key)
		if err != nil {
			continue
		}
		toCreate = append(toCreate, spec)
	}

	// Advance the removal hysteresis for Managed Forwards that are no
	// longer desired (policy mismatch, Ignore, retirement, disappearance).
	var toRemove []ForwardID
	for _, forward := range m.forwards.snapshots() {
		if forward.Kind != ForwardManaged {
			continue
		}
		key, known := managedForwardKey(forward.ID)
		if !known {
			continue
		}
		_, isRetired := retiredKeys[key]
		_, stillDesired := desired[key]
		if m.reconciler.removalStep(forward.ID, stillDesired, isRetired) {
			toRemove = append(toRemove, forward.ID)
		}
	}

	// Commit this generation's policy set before the delta check: the Ask
	// derivation reads the cache, so an edit that changed only Ask
	// verdicts must land even when no forward delta follows.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	policyChanged := m.reconciler.commitPolicies(policies)
	if len(toCreate) == 0 && len(toRemove) == 0 && len(retired) == 0 {
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
	m.reconciler.retire(retired)
	for _, owner := range created {
		if !m.forwards.add(owner) {
			_ = owner.Close(context.Background())
		}
	}
	for _, forward := range removed {
		_ = forward.owner.Close(context.Background())
		delete(m.reconciler.removalState, forward.id)
	}
	if len(created) != 0 || len(removed) != 0 || len(retired) != 0 {
		m.publishLocked()
	}
}

// managedForwardSpec is the single construction of a Managed Forward's
// allocation spec: the managed:<family>:<scope>:<port> identity format
// lives here, shared by the reconciliation worker and the approve command.
func managedForwardSpec(key remoteListenerKey) (forwardSpec, error) {
	remote, err := manualTarget(key.family, key.port)
	if err != nil {
		return forwardSpec{}, err
	}
	return forwardSpec{
		ID:                 ForwardID("managed:" + managedForwardToken(key)),
		Kind:               ForwardManaged,
		Remote:             remote,
		PreferredLocalPort: key.port,
	}, nil
}

// allocateManagedForward allocates the Local Endpoint for a Managed Forward
// serving key, through the single spec construction above. Registration is
// the caller's: the worker and the approve command differ in how they treat
// a registration race, and both close a losing owner.
func (m *manager) allocateManagedForward(ctx context.Context, key remoteListenerKey) (ownedForward, error) {
	spec, err := managedForwardSpec(key)
	if err != nil {
		return nil, err
	}
	return m.forwardAllocator.Allocate(ctx, spec)
}

// managedForwardToken builds a stable, collision-resistant Managed Forward
// identity from the listener key: reconciliation must address the same
// forward across observations and commands.
func managedForwardToken(key remoteListenerKey) string {
	return string(key.family) + ":" + string(key.scope) + ":" + strconv.Itoa(int(key.port))
}

// managedForwardKey recovers the listener key a Managed Forward serves from
// its identity (managed:<family>:<scope>:<port>). Unknown shapes (foreign
// or manual forwards) report false.
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
