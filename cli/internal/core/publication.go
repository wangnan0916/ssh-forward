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
	return Snapshot{
		Revision: m.revision,
		Hosts: []HostSnapshot{{
			Alias:                m.host,
			Connection:           m.connection,
			Discovery:            m.discovery,
			ListenerObservations: m.listenerObservations,
			Forwards:             m.forwards.snapshots(),
		}},
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
	if snapshot.Hosts == nil {
		return cloned
	}
	cloned.Hosts = make([]HostSnapshot, len(snapshot.Hosts))
	for hostIndex, host := range snapshot.Hosts {
		cloned.Hosts[hostIndex] = HostSnapshot{
			Alias:      host.Alias,
			Connection: host.Connection,
			Discovery:  host.Discovery,
		}
		if host.ListenerObservations != nil {
			cloned.Hosts[hostIndex].ListenerObservations = cloneListenerObservations(host.ListenerObservations)
		}
		if host.Forwards == nil {
			continue
		}
		cloned.Hosts[hostIndex].Forwards = make([]ForwardSnapshot, len(host.Forwards))
		for forwardIndex, forward := range host.Forwards {
			cloned.Hosts[hostIndex].Forwards[forwardIndex] = cloneForward(forward)
		}
	}
	return cloned
}

func (m *manager) Watch(ctx context.Context, _ WatchOptions) (SnapshotStream, error) {
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
