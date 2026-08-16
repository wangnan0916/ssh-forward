package core

import (
	"context"
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

// reconcileLoop is the Manager's single reconciliation worker. The actor
// signals it once per applied observation generation (notifyReconcile); the
// worker reads the mirror outside the Manager lock, evaluates policies, and
// executes the Managed Forward delta — so allocation and teardown never
// block the actor or a command. It is the only writer of the Managed
// Forward lifecycle; commands and the worker serialize through the forward
// table and the Manager lock. The drain loop consumes every buffered
// notification so each generation advances the hysteresis exactly once,
// even when the actor outruns the worker.
func (m *manager) reconcileLoop() {
	defer m.workers.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.reconcile:
			for {
				m.reconcileOnce()
				select {
				case <-m.reconcile:
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
	policies := m.policySource()
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	host := m.hostSnapshot
	approvals := cloneApprovals(m.approvals)
	suppressions := cloneSuppressions(m.suppressions)
	forwardSnapshots := m.forwards.snapshots()
	m.mu.RUnlock()

	now := m.now()
	observationByKey := make(map[remoteListenerKey]ListenerObservation, len(host.ListenerObservations))
	for _, observation := range host.ListenerObservations {
		observationByKey[listenerKey(observation)] = observation
	}
	statusByKey := make(map[remoteListenerKey]LifetimeStatus, len(host.ListenerLifetimes))
	for _, verdict := range host.ListenerLifetimes {
		statusByKey[remoteListenerKey{family: verdict.Family, scope: verdict.BindScope, port: verdict.RemotePort}] = verdict.Status
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
	var retired []remoteListenerKey
	for key := range approvals {
		switch statusByKey[key] {
		case LifetimeEnded, LifetimeReplaced:
			retired = append(retired, key)
		}
	}
	for key := range suppressions {
		switch statusByKey[key] {
		case LifetimeEnded, LifetimeReplaced:
			retired = append(retired, key)
		}
	}
	retiredKeys := make(map[remoteListenerKey]struct{}, len(retired))
	for _, key := range retired {
		retiredKeys[key] = struct{}{}
	}

	// Advance the creation hysteresis: a Managed Forward needs two
	// consecutive observations of the same auto verdict. The has-managed
	// check reads the lock-snapshot taken above, so it never races a
	// command's add.
	hasManaged := func(key remoteListenerKey) bool {
		for _, forward := range forwardSnapshots {
			if forward.Kind != ForwardManaged {
				continue
			}
			if managedKey, known := managedForwardKey(forward.ID); known && managedKey == key {
				return true
			}
		}
		return false
	}
	var toCreate []forwardSpec
	for key := range desired {
		if hasManaged(key) {
			delete(m.createState, key)
			continue
		}
		m.createState[key]++
		if m.createState[key] < 2 {
			continue
		}
		delete(m.createState, key)
		observation := observationByKey[key]
		remote, err := manualTarget(observation.Family, observation.RemotePort)
		if err != nil {
			continue
		}
		toCreate = append(toCreate, forwardSpec{
			ID:                 ForwardID("managed:" + managedForwardToken(key)),
			Kind:               ForwardManaged,
			Remote:             remote,
			PreferredLocalPort: observation.RemotePort,
		})
	}

	// Advance the removal hysteresis for Managed Forwards that are no
	// longer desired (policy mismatch, Ignore, retirement, disappearance).
	var toRemove []ForwardID
	for _, forward := range forwardSnapshots {
		if forward.Kind != ForwardManaged {
			continue
		}
		key, known := managedForwardKey(forward.ID)
		if !known {
			continue
		}
		_, stillDesired := desired[key]
		if stillDesired {
			delete(m.removalState, forward.ID)
			continue
		}
		if _, isRetired := retiredKeys[key]; isRetired {
			// A retired One-time Approval removes its forward immediately:
			// the approval's credential was scoped to the ended Lifetime.
			toRemove = append(toRemove, forward.ID)
			continue
		}
		record := m.removalState[forward.ID]
		if record == nil {
			record = &managedRemoval{}
			m.removalState[forward.ID] = record
		}
		record.absentObservations++
		if record.absentObservations == 1 {
			record.absentSince = now
		}
		if record.absentObservations >= 2 && !now.Before(record.absentSince.Add(5*time.Second)) {
			toRemove = append(toRemove, forward.ID)
		}
	}

	if len(toCreate) == 0 && len(toRemove) == 0 && len(retired) == 0 {
		return
	}

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
	m.policyCache = policies
	for _, key := range retired {
		delete(m.approvals, key)
		delete(m.suppressions, key)
		delete(m.createState, key)
	}
	for _, owner := range created {
		if !m.forwards.add(owner) {
			_ = owner.Close(context.Background())
		}
	}
	for _, forward := range removed {
		_ = forward.owner.Close(context.Background())
		delete(m.removalState, forward.id)
	}
	if len(created) != 0 || len(removed) != 0 || len(retired) != 0 {
		m.publishLocked()
	}
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

func cloneApprovals(source map[remoteListenerKey]struct{}) map[remoteListenerKey]struct{} {
	cloned := make(map[remoteListenerKey]struct{}, len(source))
	for key := range source {
		cloned[key] = struct{}{}
	}
	return cloned
}

func cloneSuppressions(source map[remoteListenerKey]struct{}) map[remoteListenerKey]struct{} {
	cloned := make(map[remoteListenerKey]struct{}, len(source))
	for key := range source {
		cloned[key] = struct{}{}
	}
	return cloned
}
