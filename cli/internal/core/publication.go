package core

import (
	"context"
	"errors"
	"sync"
)

const maxManagerWatches = 128

var (
	ErrSnapshotStreamClosed   = errors.New("snapshot stream is closed")
	ErrConcurrentSnapshotNext = errors.New("another SnapshotStream.Next call is active")
	// ErrResyncRequired is the Watch terminal for manager.resync_required.
	// jsonrpc emits it for oversized Snapshots; core does not produce it.
	ErrResyncRequired = errors.New("snapshot stream requires resynchronization")
)

type snapshotStream struct {
	manager *manager
	id      uint64
	ready   chan struct{}

	mu             sync.Mutex
	initial        Snapshot
	initialPending bool
	latest         *Snapshot
	nextActive     bool
	closed         bool
	terminal       error
}

type hostView struct {
	Alias                HostAlias
	Connection           ConnectionState
	ConnectionDiagnostic string
	Discovery            DiscoverySnapshot
	ListenerObservations []ListenerObservation
}

func emptyHostView(host HostAlias) hostView {
	return hostView{
		Alias:                host,
		Connection:           ConnectionDisconnected,
		Discovery:            stoppedDiscovery(),
		ListenerObservations: make([]ListenerObservation, 0),
	}
}

func (m *manager) composeSnapshotLocked() Snapshot {
	if m.host == "" {
		return Snapshot{Revision: m.revision}
	}
	host := HostSnapshot{
		Alias:                m.view.Alias,
		Connection:           m.view.Connection,
		ConnectionDiagnostic: m.view.ConnectionDiagnostic,
		Discovery:            m.view.Discovery,
		ListenerObservations: m.view.ListenerObservations,
		Forwards:             m.forwards.snapshots(),
		LocalPortConflicts:   m.reconciler.conflictSnapshots(),
		PolicyDiagnostic:     m.reconciler.policyDiagnostic,
	}
	return Snapshot{
		Revision: m.revision,
		Host:     &host,
	}
}

func (m *manager) publishLocked() {
	m.revision++
	m.snapshot = m.composeSnapshotLocked()
	for _, stream := range m.watchers {
		stream.publish(m.snapshot)
	}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloned := Snapshot{Revision: snapshot.Revision}
	if snapshot.Host == nil {
		return cloned
	}
	host := *snapshot.Host
	if snapshot.Host.ListenerObservations != nil {
		host.ListenerObservations = cloneListenerObservations(snapshot.Host.ListenerObservations)
	}
	if snapshot.Host.Forwards != nil {
		host.Forwards = make([]ForwardSnapshot, len(snapshot.Host.Forwards))
		for forwardIndex, forward := range snapshot.Host.Forwards {
			host.Forwards[forwardIndex] = cloneForward(forward)
		}
	}
	if snapshot.Host.LocalPortConflicts != nil {
		host.LocalPortConflicts = append([]LocalPortConflict(nil), snapshot.Host.LocalPortConflicts...)
	}
	cloned.Host = &host
	return cloned
}

func (m *manager) Watch(ctx context.Context) (SnapshotStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.closed {
		return nil, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
	}
	if len(m.watchers) >= maxManagerWatches {
		return nil, &DomainError{Kind: ErrorWatchLimit, Retryable: true}
	}
	m.nextWatchID++
	stream := &snapshotStream{
		manager:        m,
		id:             m.nextWatchID,
		ready:          make(chan struct{}, 1),
		initial:        m.snapshot,
		initialPending: true,
	}
	m.watchers[stream.id] = stream
	return stream, nil
}

func (s *snapshotStream) Next(ctx context.Context) (Snapshot, error) {
	snapshot, err := AwaitWatch(ctx, &s.mu, s.ready, &s.nextActive, func() (Snapshot, bool, error) {
		return TakeWatchSnapshot(true, s.closed, s.terminal, &s.initialPending, s.initial, &s.latest)
	})
	if err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

func AwaitWatch(ctx context.Context, mu *sync.Mutex, ready <-chan struct{}, nextActive *bool, take func() (Snapshot, bool, error)) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	mu.Lock()
	if *nextActive {
		mu.Unlock()
		return Snapshot{}, ErrConcurrentSnapshotNext
	}
	*nextActive = true
	mu.Unlock()
	defer func() {
		mu.Lock()
		*nextActive = false
		mu.Unlock()
	}()
	for {
		mu.Lock()
		snapshot, found, err := take()
		mu.Unlock()
		if err != nil {
			return Snapshot{}, err
		}
		if found {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-ready:
		}
	}
}

func TakeWatchSnapshot(closedFirst, closed bool, terminal error, initialPending *bool, initial Snapshot, latest **Snapshot) (Snapshot, bool, error) {
	if closedFirst && closed {
		return Snapshot{}, false, terminal
	}
	if *initialPending {
		*initialPending = false
		return initial, true, nil
	}
	if *latest != nil {
		snapshot := **latest
		*latest = nil
		return snapshot, true, nil
	}
	if closed {
		return Snapshot{}, false, terminal
	}
	return Snapshot{}, false, nil
}

func (s *snapshotStream) publish(snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.latest = &snapshot
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *snapshotStream) Close() error {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	s.manager.closeSnapshotStreamLocked(s.id, s, ErrSnapshotStreamClosed)
	return nil
}

func (m *manager) closeSnapshotStreamLocked(id uint64, stream *snapshotStream, terminal error) {
	if current, found := m.watchers[id]; found && current == stream {
		delete(m.watchers, id)
	}
	stream.finish(terminal)
}

func (s *snapshotStream) finish(terminal error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.terminal = terminal
	select {
	case s.ready <- struct{}{}:
	default:
	}
}
