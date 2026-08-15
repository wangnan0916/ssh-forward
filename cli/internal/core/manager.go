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
	retryDelay       func(int) time.Duration
	retryWait        func(context.Context, time.Duration) bool
}

type manager struct {
	mu               sync.RWMutex
	closed           bool
	host             HostAlias
	connector        hostConnector
	forwardAllocator forwardAllocator
	revision         Revision
	connection       ConnectionState
	forwards         forwardTable
	commands         map[CommandID]commandRecord
	pending          map[CommandID]*pendingCommand
	dialer           *currentDialer
	session          hostSession

	retryDelay func(int) time.Duration
	retryWait  func(context.Context, time.Duration) bool

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
	return &manager{
		host:             options.host,
		connector:        options.connector,
		forwardAllocator: allocator,
		connection:       ConnectionDisconnected,
		forwards:         newForwardTable(),
		commands:         make(map[CommandID]commandRecord),
		pending:          make(map[CommandID]*pendingCommand),
		dialer:           dialer,
		retryDelay:       retryDelay,
		retryWait:        retryWait,
		ctx:              ctx,
		cancel:           cancel,
	}
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
	m.revision++
	startConnection := m.connection == ConnectionDisconnected
	if startConnection {
		m.connection = ConnectionConnecting
		m.workers.Add(1)
	}
	outcome := Outcome{Kind: OutcomeForwardAdded, Revision: m.revision, Forward: cloneForward(forward)}
	m.completeCommandLocked(add.CommandID, add, outcome)
	m.mu.Unlock()

	if startConnection {
		go m.connect()
	}
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
	m.revision++
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

func (m *manager) Snapshot(context.Context, Scope) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.host == "" {
		return Snapshot{Revision: m.revision}, nil
	}
	forwards := m.forwards.snapshots()
	return Snapshot{
		Revision: m.revision,
		Hosts: []HostSnapshot{
			{
				Alias:      m.host,
				Connection: m.connection,
				Forwards:   forwards,
			},
		},
	}, nil
}

func cloneOutcome(outcome Outcome) Outcome {
	outcome.Forward = cloneForward(outcome.Forward)
	return outcome
}

func cloneForward(forward ForwardSnapshot) ForwardSnapshot {
	forward.LocalFamilies = append([]AddressFamily(nil), forward.LocalFamilies...)
	return forward
}

func (m *manager) Watch(context.Context, WatchOptions) (SnapshotStream, error) {
	return nil, errors.New("Watch is not implemented")
}

func (m *manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	forwards := m.forwards.owners()
	session := m.session
	m.mu.Unlock()

	var errs []error
	for _, forward := range forwards {
		errs = append(errs, forward.Close(ctx))
	}
	if session != nil {
		errs = append(errs, session.Close(ctx))
	}
	workersDone := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
		return errors.Join(errs...)
	case <-ctx.Done():
		return errors.Join(append(errs, ctx.Err())...)
	}
}
