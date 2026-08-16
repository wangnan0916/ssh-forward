package core

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// managerOptions is the Manager's single configuration surface — and a test
// seam: every field except host has an injected default, and the core tests
// replace them to script time, concurrency, and failure. Do not wire these
// in production code paths outside newManager. publishHost is the strongest
// knob: when it blocks, the Manager state lock blocks, and any goroutine
// waiting on it (including the actor) stalls — tests use it as a deliberate
// freeze window, production must never.
type managerOptions struct {
	host             HostAlias
	connector        hostConnector
	forwardAllocator forwardAllocator
	publishHost      func(HostSnapshot)
	retryDelay       func(int) time.Duration
	retryWait        func(context.Context, time.Duration) bool
	// policies is the Forwarding Policy source seam (slice 5): the
	// reconciliation path refreshes its policy cache from this function
	// outside the Manager lock. nil means no policies (default Ask).
	policies func() []ForwardingPolicy
	// now is the wall-clock seam for the reconciliation path's
	// five-second removal floor (decision recorded in
	// implementation-sequence.md slice 5). The Listener Lifetime tracker
	// never uses it. nil defaults to time.Now.
	now func() time.Time
}

type manager struct {
	mu               sync.RWMutex
	closed           bool
	host             HostAlias
	forwardAllocator forwardAllocator
	revision         Revision
	hostSnapshot     HostSnapshot
	actor            *hostActor
	forwards         forwardTable
	snapshot         Snapshot
	watchers         map[uint64]*snapshotStream
	nextWatchID      uint64
	commands         map[CommandID]commandRecord
	commandOrder     []CommandID
	pending          map[CommandID]*pendingCommand

	reconciler *reconciler

	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

// NewManager builds a Manager with no host and no connector: it exists for
// wire-level tests (jsonrpc fixtures) and core tests that exercise command
// plumbing without a host. Production assembly goes through
// NewConfiguredManager via app.NewManager.
func NewManager() Manager {
	return newManager(managerOptions{})
}

// NewConfiguredManager is the production seam: it wires the host, the host
// connector (the OpenSSH Adapter), and the Forwarding Policy source (slice
// 5; nil means default Ask) into the Manager. app.NewManager is its only
// production caller; tests inject through managerOptions instead.
func NewConfiguredManager(host HostAlias, connector HostConnector, policySources ...func() []ForwardingPolicy) Manager {
	var policies func() []ForwardingPolicy
	if len(policySources) != 0 {
		policies = policySources[0]
	}
	return newManager(managerOptions{host: host, connector: connector, policies: policies})
}

func newManager(options managerOptions) *manager {
	ctx, cancel := context.WithCancel(context.Background())
	retryDelay := options.retryDelay
	if retryDelay == nil {
		retryDelay = exponentialJitterDelay
	}
	retryWait := options.retryWait
	if retryWait == nil {
		retryWait = waitForRetry
	}
	dialer := &currentDialer{}
	allocator := options.forwardAllocator
	if allocator == nil {
		allocator = proxyForwardAllocator{dialer: dialer}
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	policySource := options.policies
	if policySource == nil {
		policySource = func() []ForwardingPolicy { return nil }
	}
	m := &manager{
		host:             options.host,
		forwardAllocator: allocator,
		forwards:         newForwardTable(),
		watchers:         make(map[uint64]*snapshotStream),
		commands:         make(map[CommandID]commandRecord),
		commandOrder:     make([]CommandID, 0, maxCommandRecords),
		pending:          make(map[CommandID]*pendingCommand),
		reconciler:       newReconciler(policySource, now),
		hostSnapshot:     emptyHostSnapshot(options.host),
		ctx:              ctx,
		cancel:           cancel,
	}
	m.workers.Add(1)
	go m.reconcileLoop()
	publishHost := m.publishHostState
	if options.publishHost != nil {
		publishHost = options.publishHost
	}
	m.actor = newHostActor(hostActorOptions{
		host:          options.host,
		connector:     options.connector,
		dialer:        dialer,
		publish:       publishHost,
		onObservation: m.reconciler.notify,
		ctx:           ctx,
	}, retryDelay, retryWait)
	m.snapshot = m.buildSnapshotLocked()
	return m
}

// emptyHostSnapshot is the single construction of the pre-connection state
// shape: the Manager seeds its mirror with it, and the hostActor's internal
// state starts from it, so the two cannot drift in initial shape.
func emptyHostSnapshot(host HostAlias) HostSnapshot {
	return HostSnapshot{
		Alias:                host,
		Connection:           ConnectionDisconnected,
		Discovery:            stoppedDiscovery(),
		ListenerObservations: make([]ListenerObservation, 0),
	}
}

// publishHostState is the actor's publication path into the Manager's mirror:
// it replaces the mirror wholesale and publishes. beginConnectionLocked is the
// command path's declaration — it patches the mirror to Connecting under the
// Manager lock and lets the caller publish once with the command outcome, so
// the transition is visible in the same revision as the command result. Both
// write under the Manager lock and both treat HostSnapshot as the single
// state structure; the actor takes over state evolution from Connected on.
// beginConnectionLocked is the command path's Connecting declaration: it
// patches the mirror under the Manager lock so the transition is visible in
// the same Revision as the command outcome. This is the one place the
// Manager writes the mirror directly — structurally forced by three
// constraints: (1) the actor lock can never be taken inside the Manager lock
// (the actor publishes under the Manager lock, so the reverse order would
// deadlock), (2) the command outcome must share a Revision with Connecting
// (UI contract pinned by tests), and (3) commands must never block on the
// arming publication (round-4 race-free arming). The guard reads the actor's
// own armed() projection instead of the mirror state: the mirror is
// Disconnected exactly while the actor is unarmed, because every
// active=false write lands in the same critical section as the Disconnected
// publication.
func (m *manager) beginConnectionLocked() {
	if m.actor.armed() {
		return
	}
	declared := m.hostSnapshot
	declared.Connection = ConnectionConnecting
	m.hostSnapshot = declared
}

func (m *manager) publishHostState(state HostSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.hostSnapshot = state
	m.publishLocked()
}

func (m *manager) Execute(ctx context.Context, command Command) (Outcome, error) {
	switch command := command.(type) {
	case AddManualForward:
		return m.addManualForward(ctx, command)
	case RemoveForward:
		return m.removeForward(ctx, command)
	case ApproveListener:
		return m.approveListener(ctx, command)
	case SuppressListener:
		return m.suppressListener(ctx, command)
	default:
		return Outcome{}, &DomainError{Kind: ErrorInvalidCommand}
	}
}

func (m *manager) addManualForward(ctx context.Context, add AddManualForward) (Outcome, error) {
	if outcome, settled, err := m.admitCommand(ctx, add.CommandID, add, add.Host); settled {
		return outcome, err
	}
	remote, err := manualTarget(add.Family, add.RemotePort)
	if err != nil {
		m.failCommandAndRelease(add.CommandID)
		return Outcome{}, err
	}

	owner, err := m.forwardAllocator.Allocate(ctx, forwardSpec{
		ID:                 ForwardID("manual:" + string(add.CommandID)),
		Kind:               ForwardManual,
		Remote:             remote,
		PreferredLocalPort: add.RemotePort,
	})
	if err != nil {
		m.failCommandAndRelease(add.CommandID)
		// Local Port Conflict is already the domain error from the allocator
		// seam; pass it through unchanged.
		return Outcome{}, err
	}
	forward := owner.Projection()

	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		closed := m.closed
		m.failCommandLockedAndReleaseForward(add.CommandID, owner)
		if closed {
			return Outcome{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
		}
		return Outcome{}, ctx.Err()
	}
	if !m.forwards.add(owner) {
		m.failCommandLockedAndReleaseForward(add.CommandID, owner)
		return Outcome{}, &DomainError{Kind: ErrorCommandIDConflict}
	}
	m.beginConnectionLocked()
	m.publishLocked()
	outcome := Outcome{Kind: OutcomeForwardAdded, Revision: m.revision, Forward: cloneForward(forward)}
	m.mu.Unlock()
	m.completeCommand(add.CommandID, add, outcome)

	// Always arm: the actor re-checks its liveness under its own lock, so a
	// command racing the actor's terminal publication (which sets active
	// false before publishing Disconnected) still re-arms once that
	// publication lands. beginConnectionLocked may have declined to publish
	// Connecting because it read a stale Connected copy; the actor's arming
	// decision is authoritative regardless.
	m.actor.startIfNeeded()
	return outcome, nil
}

func (m *manager) removeForward(ctx context.Context, remove RemoveForward) (Outcome, error) {
	if outcome, replayed, err := m.beginCommand(ctx, remove.CommandID, remove); replayed || err != nil {
		return outcome, err
	}
	owner, forward, err := m.reserveRemoval(ctx, remove)
	if err != nil {
		m.failCommandAndRelease(remove.CommandID)
		return Outcome{}, err
	}

	closed := make(chan error, 1)
	go func() {
		closed <- owner.Close(context.Background())
	}()
	select {
	case closeErr := <-closed:
		outcome := m.completeRemoval(remove, forward)
		m.completeCommand(remove.CommandID, remove, outcome)
		if closeErr != nil {
			return Outcome{}, closeErr
		}
		return outcome, nil
	case <-ctx.Done():
		go func() {
			<-closed
			m.completeCommand(remove.CommandID, remove, m.completeRemoval(remove, forward))
		}()
		return Outcome{}, ctx.Err()
	}
}

func (m *manager) reserveRemoval(ctx context.Context, remove RemoveForward) (ownedForward, ForwardSnapshot, error) {
	for {
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return nil, ForwardSnapshot{}, err
		}
		if m.closed {
			m.mu.Unlock()
			return nil, ForwardSnapshot{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
		}
		owner, forward, operationID, state := m.forwards.reserveRemoval(remove.ForwardID, remove.CommandID)
		switch state {
		case removalAvailable:
			m.mu.Unlock()
			return owner, cloneForward(forward), nil
		case removalInProgress:
			pending := m.pending[operationID]
			if pending == nil {
				m.mu.Unlock()
				return nil, ForwardSnapshot{}, &DomainError{Kind: ErrorForwardNotFound}
			}
			if err := m.waitForPendingCommand(ctx, pending); err != nil {
				return nil, ForwardSnapshot{}, err
			}
			continue
		default:
			m.mu.Unlock()
			return nil, ForwardSnapshot{}, &DomainError{Kind: ErrorForwardNotFound}
		}
	}
}

func (m *manager) completeRemoval(remove RemoveForward, forward ForwardSnapshot) Outcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	if removed, found := m.forwards.completeRemoval(remove.ForwardID, remove.CommandID); found {
		forward = removed
	}
	m.publishLocked()
	return Outcome{Kind: OutcomeForwardRemoved, Revision: m.revision, Forward: cloneForward(forward)}
}

// ApproveListener records a One-time Approval for the current Listener
// Lifetime and immediately creates its Managed Forward (the user's explicit
// intent needs no hysteresis). If the listener already has a Managed
// Forward, only the approval is recorded.
func (m *manager) approveListener(ctx context.Context, approve ApproveListener) (Outcome, error) {
	if outcome, settled, err := m.admitCommand(ctx, approve.CommandID, approve, approve.Host); settled {
		return outcome, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.failCommandAndRelease(approve.CommandID)
		return Outcome{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
	}
	key, _, found := m.findListenerLocked(approve.RemotePort, approve.Family)
	if !found {
		m.mu.Unlock()
		m.failCommandAndRelease(approve.CommandID)
		return Outcome{}, &DomainError{Kind: ErrorListenerNotFound}
	}
	m.reconciler.approvals[key] = struct{}{}
	if m.forwards.hasManagedForListener(key) {
		return m.completeDecisionLocked(approve.CommandID, approve, Outcome{Kind: OutcomeApprovalRecorded, Revision: m.revision}), nil
	}
	m.mu.Unlock()

	owner, err := m.allocateManagedForward(ctx, key)
	if err != nil {
		m.mu.Lock()
		delete(m.reconciler.approvals, key)
		m.mu.Unlock()
		m.failCommandAndRelease(approve.CommandID)
		return Outcome{}, err
	}
	forward := owner.Projection()

	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		closed := m.closed
		m.failCommandLockedAndReleaseForward(approve.CommandID, owner)
		m.mu.Unlock()
		if closed {
			return Outcome{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
		}
		return Outcome{}, ctx.Err()
	}
	if !m.forwards.add(owner) {
		// The reconciliation worker registered the same Managed Forward
		// first: the approval's intent is already served, so the outcome
		// is the approval record — not a command conflict.
		_ = owner.Close(context.Background())
		return m.completeDecisionLocked(approve.CommandID, approve, Outcome{Kind: OutcomeApprovalRecorded, Revision: m.revision}), nil
	}
	m.publishLocked()
	outcome := Outcome{Kind: OutcomeApprovalRecorded, Revision: m.revision, Forward: cloneForward(forward)}
	m.mu.Unlock()
	m.completeCommand(approve.CommandID, approve, outcome)
	return outcome, nil
}

// SuppressListener records a One-time Suppression for the current Listener
// Lifetime: the listener leaves the Ask list until the lifetime ends.
func (m *manager) suppressListener(ctx context.Context, suppress SuppressListener) (Outcome, error) {
	if outcome, settled, err := m.admitCommand(ctx, suppress.CommandID, suppress, suppress.Host); settled {
		return outcome, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.failCommandAndRelease(suppress.CommandID)
		return Outcome{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
	}
	key, _, found := m.findListenerLocked(suppress.RemotePort, suppress.Family)
	if !found {
		m.mu.Unlock()
		m.failCommandAndRelease(suppress.CommandID)
		return Outcome{}, &DomainError{Kind: ErrorListenerNotFound}
	}
	m.reconciler.suppressions[key] = struct{}{}
	return m.completeDecisionLocked(suppress.CommandID, suppress, Outcome{Kind: OutcomeSuppressionRecorded, Revision: m.revision}), nil
}

// admitCommand runs the shared admission for the resource commands: the
// replay check and the host guard. It reports whether the command was
// already settled — a replayed answer from the journal or a failed
// admission — and the caller must return (outcome, err) unchanged in that
// case.
func (m *manager) admitCommand(ctx context.Context, id CommandID, command Command, host HostAlias) (Outcome, bool, error) {
	if outcome, replayed, err := m.beginCommand(ctx, id, command); replayed || err != nil {
		return outcome, true, err
	}
	if host != m.host || m.host == "" {
		m.failCommandAndRelease(id)
		return Outcome{}, true, &DomainError{Kind: ErrorUnknownHost}
	}
	return Outcome{}, false, nil
}

// completeDecisionLocked publishes the mirror, releases the Manager lock,
// and completes the journal record for a recorded One-time decision. The
// caller must hold the Manager lock.
func (m *manager) completeDecisionLocked(id CommandID, command Command, outcome Outcome) Outcome {
	m.publishLocked()
	m.mu.Unlock()
	m.completeCommand(id, command, outcome)
	return outcome
}

// findListenerLocked locates the Listener the commands target: an exact
// family when one is given (FamilyAuto or empty matches the first listener
// on the port, deterministically — observations are canonically ordered).
func (m *manager) findListenerLocked(port uint16, family AddressFamily) (remoteListenerKey, ListenerObservation, bool) {
	for _, observation := range m.hostSnapshot.ListenerObservations {
		if observation.RemotePort != port {
			continue
		}
		if family != FamilyAuto && family != "" && observation.Family != family {
			continue
		}
		return listenerKey(observation), observation, true
	}
	return remoteListenerKey{}, ListenerObservation{}, false
}

// manualTarget is the authoritative defense for a Manual Forward's target:
// the IPC Adapter pre-checks RemotePort == 0 and the family as wire-invalid
// parameters, and this same port-zero and family rule is re-enforced here so
// the command path cannot construct an invalid loopback target regardless of
// adapter.
func manualTarget(family AddressFamily, port uint16) (netip.AddrPort, error) {
	if port == 0 || !ValidAddressFamily(family) {
		return netip.AddrPort{}, &DomainError{Kind: ErrorInvalidCommand}
	}
	switch family {
	case FamilyAuto, FamilyIPv4:
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port), nil
	case FamilyIPv6:
		return netip.AddrPortFrom(netip.IPv6Loopback(), port), nil
	default:
		return netip.AddrPort{}, &DomainError{Kind: ErrorInvalidCommand}
	}
}

func familyForAddress(address netip.Addr) AddressFamily {
	if address.Is4() {
		return FamilyIPv4
	}
	return FamilyIPv6
}

func (m *manager) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSnapshot(m.snapshot), nil
}

func cloneOutcome(outcome Outcome) Outcome {
	outcome.Forward = cloneForward(outcome.Forward)
	return outcome
}

func cloneForward(forward ForwardSnapshot) ForwardSnapshot {
	forward.LocalFamilies = append([]AddressFamily(nil), forward.LocalFamilies...)
	return forward
}

func (m *manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		for id, stream := range m.watchers {
			m.closeSnapshotStreamLocked(id, stream, &DomainError{Kind: ErrorManagerClosed, Retryable: true})
		}
	}
	forwards := m.forwards.owners()
	m.mu.Unlock()

	var errs []error
	for _, forward := range forwards {
		errs = append(errs, forward.Close(ctx))
	}
	errs = append(errs, m.actor.closeSession(ctx))
	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		if m.actor.isActive() {
			<-m.actor.done
		}
		close(done)
	}()
	select {
	case <-done:
		return errors.Join(errs...)
	case <-ctx.Done():
		return errors.Join(append(errs, ctx.Err())...)
	}
}
