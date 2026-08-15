package core

import (
	"cmp"
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"time"

	"ssh-forward/cli/internal/proxy"
)

type managerOptions struct {
	host       HostAlias
	connector  hostConnector
	retryDelay func(int) time.Duration
	retryWait  func(context.Context, time.Duration) bool
}

type manager struct {
	mu         sync.RWMutex
	closed     bool
	host       HostAlias
	connector  hostConnector
	revision   Revision
	connection ConnectionState
	forwards   []ForwardSnapshot
	endpoints  []*proxy.Endpoint
	commands   map[CommandID]commandRecord
	pending    map[CommandID]*pendingCommand
	removing   map[ForwardID]CommandID
	dialer     *currentDialer
	session    hostSession

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
	return &manager{
		host:       options.host,
		connector:  options.connector,
		connection: ConnectionDisconnected,
		commands:   make(map[CommandID]commandRecord),
		pending:    make(map[CommandID]*pendingCommand),
		removing:   make(map[ForwardID]CommandID),
		dialer:     &currentDialer{},
		retryDelay: retryDelay,
		retryWait:  retryWait,
		ctx:        ctx,
		cancel:     cancel,
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

	endpoint, err := proxy.OpenEndpoint(proxy.EndpointOptions{
		PreferredPort: add.RemotePort,
		Remote:        remote,
		Dialer:        m.dialer,
	})
	if err != nil {
		m.failCommand(add.CommandID)
		if errors.Is(err, proxy.ErrLocalPortConflict) {
			return Outcome{}, &DomainError{Kind: ErrorLocalPortConflict, Retryable: true}
		}
		return Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		closeEndpoint(endpoint)
		m.failCommand(add.CommandID)
		return Outcome{}, err
	}
	forward := ForwardSnapshot{
		ID:                 ForwardID("manual:" + string(add.CommandID)),
		Kind:               ForwardManual,
		RemotePort:         add.RemotePort,
		RemoteFamily:       familyForAddress(remote.Addr()),
		AllocatedLocalPort: endpoint.LocalPort(),
		LocalFamilies:      []AddressFamily{FamilyIPv4, FamilyIPv6},
	}

	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		closed := m.closed
		m.failCommandLocked(add.CommandID)
		m.mu.Unlock()
		closeEndpoint(endpoint)
		if closed {
			return Outcome{}, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
		}
		return Outcome{}, ctx.Err()
	}
	m.endpoints = append(m.endpoints, endpoint)
	m.forwards = append(m.forwards, forward)
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
	endpoint, forward, err := m.reserveRemoval(ctx, remove)
	if err != nil {
		m.failCommand(remove.CommandID)
		m.workers.Done()
		return Outcome{}, err
	}

	closed := make(chan error, 1)
	go func() {
		closed <- endpoint.Close(context.Background())
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

func (m *manager) reserveRemoval(ctx context.Context, remove RemoveForward) (*proxy.Endpoint, ForwardSnapshot, error) {
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
		if operationID, found := m.removing[remove.ForwardID]; found {
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
		}
		for index, forward := range m.forwards {
			if forward.ID == remove.ForwardID {
				m.removing[remove.ForwardID] = remove.CommandID
				m.mu.Unlock()
				return m.endpoints[index], cloneForward(forward), nil
			}
		}
		m.mu.Unlock()
		return nil, ForwardSnapshot{}, &DomainError{Kind: ErrorForwardNotFound}
	}
}

func (m *manager) completeRemoval(remove RemoveForward, forward ForwardSnapshot) Outcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, candidate := range m.forwards {
		if candidate.ID == remove.ForwardID {
			m.forwards = append(m.forwards[:index], m.forwards[index+1:]...)
			m.endpoints = append(m.endpoints[:index], m.endpoints[index+1:]...)
			break
		}
	}
	delete(m.removing, remove.ForwardID)
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
	forwards := make([]ForwardSnapshot, len(m.forwards))
	for index, forward := range m.forwards {
		forwards[index] = cloneForward(forward)
	}
	slices.SortFunc(forwards, func(left, right ForwardSnapshot) int {
		return cmp.Compare(left.ID, right.ID)
	})
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
	endpoints := append([]*proxy.Endpoint(nil), m.endpoints...)
	session := m.session
	m.mu.Unlock()

	var errs []error
	for _, endpoint := range endpoints {
		errs = append(errs, endpoint.Close(ctx))
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
