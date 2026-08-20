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
// for oversized Snapshots — but the error stays wired so a later durable
// journal can consume it without changing the Adapter.
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

// hostView is the actor's slice of a HostSnapshot: Connection,
// Connection Diagnostic, Discovery, and Listener Observations.
// composeSnapshotLocked overlays Forwards, Local Port Conflicts, and
// Policy Diagnostic, because the actor overwrites this mirror wholesale.
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

// composeSnapshotLocked is the single construction of an immutable Snapshot:
// the actor's host view plus the Forward table plus Local Port Conflicts.
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
	// Closed wins over the initial Snapshot. remoteStream in jsonrpc still
	// delivers the subscribe Snapshot first; do not merge the two without a
	// test that picks one.
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

// closeSnapshotStreamLocked tears down one stream while the caller holds
// the Manager lock: remove it from the watcher registry and finish it with
// the terminal error. finish's guard keeps the per-stream single-terminal
// guarantee regardless of caller.
func (m *manager) closeSnapshotStreamLocked(id uint64, stream *snapshotStream, terminal error) {
	if current, found := m.watchers[id]; found && current == stream {
		delete(m.watchers, id)
	}
	stream.finish(terminal)
}

func (m *manager) closeSnapshotStream(id uint64, stream *snapshotStream, terminal error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeSnapshotStreamLocked(id, stream, terminal)
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
