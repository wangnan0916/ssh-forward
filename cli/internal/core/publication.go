package core

import (
	"context"
	"errors"
	"sync"
)

const maxManagerWatches = 128

// ErrResyncRequired is the Manager→Adapter resync channel promised by the
// IPC protocol ("Manager-required resync"): a SnapshotStream may end with it
// when core can no longer deliver a consistent sequence to the watcher.
// Core has no producer today — the jsonrpc Adapter only emits resync itself
// for oversized Snapshots — but persistence replay (slice 5) is expected to
// consume this channel, so it stays wired end to end.
var (
	ErrSnapshotStreamClosed   = errors.New("snapshot stream is closed")
	ErrConcurrentSnapshotNext = errors.New("another SnapshotStream.Next call is active")
	ErrResyncRequired         = errors.New("snapshot stream requires resynchronization")
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

func (m *manager) buildSnapshotLocked() Snapshot {
	if m.host == "" {
		return Snapshot{Revision: m.revision}
	}
	host := m.hostSnapshot
	host.Forwards = m.forwards.snapshots()
	return Snapshot{
		Revision: m.revision,
		Host:     &host,
	}
}

func (m *manager) publishLocked() {
	m.revision++
	m.snapshot = m.buildSnapshotLocked()
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
	if snapshot.Host.ListenerLifetimes != nil {
		host.ListenerLifetimes = append([]ListenerLifetimeSnapshot(nil), snapshot.Host.ListenerLifetimes...)
	}
	if snapshot.Host.Forwards != nil {
		host.Forwards = make([]ForwardSnapshot, len(snapshot.Host.Forwards))
		for forwardIndex, forward := range snapshot.Host.Forwards {
			host.Forwards[forwardIndex] = cloneForward(forward)
		}
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
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if !s.beginNext() {
		return Snapshot{}, ErrConcurrentSnapshotNext
	}
	defer s.endNext()
	for {
		s.mu.Lock()
		snapshot, found, err := s.nextLocked()
		s.mu.Unlock()
		if err != nil {
			return Snapshot{}, err
		}
		if found {
			return cloneSnapshot(snapshot), nil
		}
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-s.ready:
		}
	}
}

func (s *snapshotStream) beginNext() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextActive {
		return false
	}
	s.nextActive = true
	return true
}

func (s *snapshotStream) endNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextActive = false
}

func (s *snapshotStream) nextLocked() (Snapshot, bool, error) {
	if s.closed {
		return Snapshot{}, false, s.terminal
	}
	if s.initialPending {
		s.initialPending = false
		return s.initial, true, nil
	}
	if s.latest != nil {
		snapshot := *s.latest
		s.latest = nil
		return snapshot, true, nil
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
	s.manager.closeSnapshotStream(s.id, s, ErrSnapshotStreamClosed)
	return nil
}

func (m *manager) closeSnapshotStream(id uint64, stream *snapshotStream, terminal error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
