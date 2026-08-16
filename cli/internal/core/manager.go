package core

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

type managerOptions struct {
	host             HostAlias
	connector        hostConnector
	forwardAllocator forwardAllocator
	publishHost      func(HostSnapshot)
	retryDelay       func(int) time.Duration
	retryWait        func(context.Context, time.Duration) bool
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
	pending          map[CommandID]*pendingCommand

	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

func NewManager() Manager {
	return newManager(managerOptions{})
}

func NewConfiguredManager(host HostAlias, connector HostConnector) Manager {
	return newManager(managerOptions{host: host, connector: connector})
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
	m := &manager{
		host:             options.host,
		forwardAllocator: allocator,
		forwards:         newForwardTable(),
		watchers:         make(map[uint64]*snapshotStream),
		commands:         make(map[CommandID]commandRecord),
		pending:          make(map[CommandID]*pendingCommand),
		hostSnapshot:     emptyHostSnapshot(options.host),
		ctx:              ctx,
		cancel:           cancel,
	}
	publishHost := m.publishHostState
	if options.publishHost != nil {
		publishHost = options.publishHost
	}
	m.actor = newHostActor(hostActorOptions{
		host:       options.host,
		connector:  options.connector,
		dialer:     dialer,
		publish:    publishHost,
		retryDelay: retryDelay,
		retryWait:  retryWait,
		ctx:        ctx,
	})
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
func (m *manager) beginConnectionLocked() {
	if m.hostSnapshot.Connection != ConnectionDisconnected {
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
	default:
		return Outcome{}, &DomainError{Kind: ErrorInvalidCommand}
	}
}

func (m *manager) addManualForward(ctx context.Context, add AddManualForward) (Outcome, error) {
	if outcome, replayed, err := m.beginCommand(ctx, add.CommandID, add); replayed || err != nil {
		return outcome, err
	}
	defer m.workers.Done()
	if add.Host != m.host || m.host == "" {
		m.failCommand(add.CommandID)
		return Outcome{}, &DomainError{Kind: ErrorUnknownHost}
	}
	remote, err := manualTarget(add.Family, add.RemotePort)
	if err != nil {
		m.failCommand(add.CommandID)
		return Outcome{}, err
	}

	owner, err := m.forwardAllocator.Allocate(ctx, forwardSpec{
		ID:                 ForwardID("manual:" + string(add.CommandID)),
		Kind:               ForwardManual,
		Remote:             remote,
		PreferredLocalPort: add.RemotePort,
	})
	if err != nil {
		m.failCommand(add.CommandID)
		if errors.Is(err, errLocalEndpointConflict) {
			return Outcome{}, &DomainError{Kind: ErrorLocalPortConflict, Retryable: true}
		}
		return Outcome{}, err
	}
	forward := owner.Projection()

	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		closed := m.closed
		m.failCommandLocked(add.CommandID)
		m.mu.Unlock()
		closeOwnedForward(owner)
		if closed {
			return Outcome{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
		}
		return Outcome{}, ctx.Err()
	}
	if !m.forwards.add(owner) {
		m.failCommandLocked(add.CommandID)
		m.mu.Unlock()
		closeOwnedForward(owner)
		return Outcome{}, &DomainError{Kind: ErrorCommandIDConflict}
	}
	m.beginConnectionLocked()
	m.publishLocked()
	outcome := Outcome{Kind: OutcomeForwardAdded, Revision: m.revision, Forward: cloneForward(forward)}
	m.completeCommandLocked(add.CommandID, add, outcome)
	m.mu.Unlock()

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
		m.failCommand(remove.CommandID)
		m.workers.Done()
		return Outcome{}, err
	}

	closed := make(chan error, 1)
	go func() {
		closed <- owner.Close(context.Background())
	}()
	select {
	case closeErr := <-closed:
		outcome := m.completeRemoval(remove, forward)
		m.workers.Done()
		if closeErr != nil {
			return Outcome{}, closeErr
		}
		return outcome, nil
	case <-ctx.Done():
		go func() {
			<-closed
			m.completeRemoval(remove, forward)
			m.workers.Done()
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
			m.mu.Unlock()
			if pending == nil {
				return nil, ForwardSnapshot{}, &DomainError{Kind: ErrorForwardNotFound}
			}
			select {
			case <-ctx.Done():
				return nil, ForwardSnapshot{}, ctx.Err()
			case <-pending.done:
				continue
			}
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
	outcome := Outcome{Kind: OutcomeForwardRemoved, Revision: m.revision, Forward: cloneForward(forward)}
	m.completeCommandLocked(remove.CommandID, remove, outcome)
	return outcome
}

func manualTarget(family AddressFamily, port uint16) (netip.AddrPort, error) {
	if port == 0 {
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
			delete(m.watchers, id)
			stream.finish(&DomainError{Kind: ErrorManagerClosed, Retryable: true})
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
