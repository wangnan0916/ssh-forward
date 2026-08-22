package jsonrpc_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type watchErrorManager struct {
	*snapshotManager
	err error
}

func (m *watchErrorManager) Watch(context.Context) (core.SnapshotStream, error) {
	return nil, m.err
}

type watchManager struct {
	*snapshotManager
	stream core.SnapshotStream
}

func (m *watchManager) Watch(context.Context) (core.SnapshotStream, error) {
	return m.stream, nil
}

type multiWatchManager struct {
	*snapshotManager
	mu      sync.Mutex
	streams []*scriptedSnapshotStream
}

func (m *multiWatchManager) Watch(context.Context) (core.SnapshotStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: core.Revision(len(m.streams) + 1)})
	m.streams = append(m.streams, stream)
	return stream, nil
}

type scriptedSnapshotStream struct {
	snapshots chan core.Snapshot
	closed    chan struct{}
	closeOnce sync.Once
}

func newScriptedSnapshotStream(snapshots ...core.Snapshot) *scriptedSnapshotStream {
	stream := &scriptedSnapshotStream{
		snapshots: make(chan core.Snapshot, len(snapshots)+1),
		closed:    make(chan struct{}),
	}
	for _, snapshot := range snapshots {
		stream.snapshots <- snapshot
	}
	return stream
}

func (s *scriptedSnapshotStream) Next(ctx context.Context) (core.Snapshot, error) {
	select {
	case snapshot := <-s.snapshots:
		return snapshot, nil
	case <-s.closed:
		return core.Snapshot{}, core.ErrSnapshotStreamClosed
	case <-ctx.Done():
		return core.Snapshot{}, ctx.Err()
	}
}

func (s *scriptedSnapshotStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type controlledErrorStream struct {
	mu      sync.Mutex
	initial bool
	release chan struct{}
	err     error
}

func newControlledErrorStream(err error) *controlledErrorStream {
	return &controlledErrorStream{initial: true, release: make(chan struct{}), err: err}
}

func (s *controlledErrorStream) Next(ctx context.Context) (core.Snapshot, error) {
	s.mu.Lock()
	if s.initial {
		s.initial = false
		s.mu.Unlock()
		return core.Snapshot{Revision: 1}, nil
	}
	s.mu.Unlock()
	select {
	case <-s.release:
		return core.Snapshot{}, s.err
	case <-ctx.Done():
		return core.Snapshot{}, ctx.Err()
	}
}

func (*controlledErrorStream) Close() error { return nil }

type staleResultSnapshotStream struct {
	mu      sync.Mutex
	initial bool
	waiting chan struct{}
	release chan struct{}
}

func newStaleResultSnapshotStream() *staleResultSnapshotStream {
	return &staleResultSnapshotStream{
		initial: true,
		waiting: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *staleResultSnapshotStream) Next(ctx context.Context) (core.Snapshot, error) {
	s.mu.Lock()
	if s.initial {
		s.initial = false
		s.mu.Unlock()
		return core.Snapshot{Revision: 1}, nil
	}
	select {
	case <-s.waiting:
	default:
		close(s.waiting)
	}
	s.mu.Unlock()
	select {
	case <-s.release:
		return core.Snapshot{Revision: 2}, nil
	case <-ctx.Done():
		return core.Snapshot{}, ctx.Err()
	}
}

// Close cannot revoke a result already committed by the in-flight Next.
func (*staleResultSnapshotStream) Close() error { return nil }

func startWatchSession(t *testing.T, stream core.SnapshotStream) *testSession {
	t.Helper()
	manager := &watchManager{snapshotManager: &snapshotManager{}, stream: stream}
	session := newTestSessionWithManager(t, manager)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.watch"}`)
	want := `{"jsonrpc":"2.0","id":"1","result":{"watch_id":"watch-1","snapshot":{"revision":1}}}`
	assertJSONEqual(t, response, []byte(want))
	return session
}

func assertNoFrameWithin(t *testing.T, session *testSession, what string) {
	t.Helper()
	if err := session.client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}
	if frame, err := session.reader.ReadBytes('\n'); err == nil {
		t.Fatalf("%s: %s", what, frame)
	}
	if err := session.client.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear client read deadline: %v", err)
	}
}

func TestServeStreamsSnapshotNotifications(t *testing.T) {
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
	session := startWatchSession(t, stream)
	stream.snapshots <- core.Snapshot{Revision: 2}
	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read Snapshot notification: %v", err)
	}
	want := `{"jsonrpc":"2.0","method":"manager.snapshot","params":{"watch_id":"watch-1","snapshot":{"revision":2}}}`
	assertJSONEqual(t, notification, []byte(want))
}

func TestServeDoesNotNotifyAfterUnwatchResponse(t *testing.T) {
	stream := newStaleResultSnapshotStream()
	session := startWatchSession(t, stream)
	select {
	case <-stream.waiting:
	case <-time.After(time.Second):
		t.Fatal("Watch worker did not request its next Snapshot")
	}
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	want := `{"jsonrpc":"2.0","id":"2","result":{"watch_id":"watch-1","stopped":true}}`
	assertJSONEqual(t, response, []byte(want))
	close(stream.release)
	assertNoFrameWithin(t, session, "notification followed unwatch response")
}

func TestServeForwardsManagerResyncRequirement(t *testing.T) {
	stream := newControlledErrorStream(core.ErrResyncRequired)
	session := startWatchSession(t, stream)
	close(stream.release)
	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read resync notification: %v", err)
	}
	want := `{"jsonrpc":"2.0","method":"manager.resync_required","params":{"watch_id":"watch-1","reason":"manager_resync_required"}}`
	assertJSONEqual(t, notification, []byte(want))
}

func TestServeEndsWatchSilentlyOnOrdinaryStreamEnd(t *testing.T) {
	stream := newControlledErrorStream(core.ErrSnapshotStreamClosed)
	session := startWatchSession(t, stream)
	close(stream.release)
	assertNoFrameWithin(t, session, "stream close emitted a frame")
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	want := `{"jsonrpc":"2.0","id":"2","result":{"watch_id":"watch-1","stopped":false}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeRequestsResyncInsteadOfSendingOversizedSnapshot(t *testing.T) {
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
	session := startWatchSession(t, stream)
	stream.snapshots <- core.Snapshot{
		Revision: 2,
		Host:     &core.HostSnapshot{Alias: core.HostAlias(strings.Repeat("x", 1<<20))},
	}
	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read resync notification: %v", err)
	}
	want := `{"jsonrpc":"2.0","method":"manager.resync_required","params":{"watch_id":"watch-1","reason":"snapshot_too_large"}}`
	assertJSONEqual(t, notification, []byte(want))
}

func TestServeClosesWatchesWhenConnectionEnds(t *testing.T) {
	manager := &multiWatchManager{snapshotManager: &snapshotManager{}}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.watch"}`)
	session.cancel()
	_ = session.client.Close()
	if err := session.wait(); err != nil {
		t.Fatalf("Serve after connection close: %v", err)
	}
	manager.mu.Lock()
	stream := manager.streams[0]
	manager.mu.Unlock()
	select {
	case <-stream.closed:
	case <-time.After(time.Second):
		t.Fatal("connection close did not close Snapshot stream")
	}
}

func TestServeSupportsMultipleWatchesWithoutAdapterSpecificLimit(t *testing.T) {
	manager := &multiWatchManager{snapshotManager: &snapshotManager{}}
	session := newTestSessionWithManager(t, manager)
	first := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.watch"}`)
	assertJSONEqual(t, first, []byte(`{"jsonrpc":"2.0","id":"1","result":{"watch_id":"watch-1","snapshot":{"revision":1}}}`))
	second := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.watch"}`)
	assertJSONEqual(t, second, []byte(`{"jsonrpc":"2.0","id":"2","result":{"watch_id":"watch-2","snapshot":{"revision":2}}}`))
}

func TestServeMapsManagerWatchLimit(t *testing.T) {
	manager := &watchErrorManager{
		snapshotManager: &snapshotManager{},
		err:             &core.DomainError{Kind: core.ErrorWatchLimit, Retryable: true},
	}
	session := newTestSessionWithManager(t, manager)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.watch"}`)
	want := `{"jsonrpc":"2.0","id":"1","error":{"code":-32015,"message":"too many active Watches","data":{"kind":"watch_limit","retryable":true}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeUnwatchIsIdempotentAndStopsStream(t *testing.T) {
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
	session := startWatchSession(t, stream)
	first := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	assertJSONEqual(t, first, []byte(`{"jsonrpc":"2.0","id":"2","result":{"watch_id":"watch-1","stopped":true}}`))
	second := session.exchange(t, `{"jsonrpc":"2.0","id":"3","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	assertJSONEqual(t, second, []byte(`{"jsonrpc":"2.0","id":"3","result":{"watch_id":"watch-1","stopped":false}}`))
	if _, err := stream.Next(context.Background()); !errors.Is(err, core.ErrSnapshotStreamClosed) {
		t.Fatalf("Snapshot stream after unwatch = %v, want closed", err)
	}
}
