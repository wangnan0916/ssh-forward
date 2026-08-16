package jsonrpc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ssh-forward/cli/internal/core"
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

type watchBlockingManager struct {
	*blockingManager
	stream core.SnapshotStream
}

func (m *watchBlockingManager) Watch(context.Context) (core.SnapshotStream, error) {
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

// Close cannot revoke the result already committed by the in-flight Next.
func (*staleResultSnapshotStream) Close() error { return nil }

type errorSnapshotStream struct {
	mu     sync.Mutex
	first  bool
	closed chan struct{}
	err    error
}

func newErrorSnapshotStream(err error) *errorSnapshotStream {
	return &errorSnapshotStream{first: true, closed: make(chan struct{}), err: err}
}

func (s *errorSnapshotStream) Next(context.Context) (core.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first {
		s.first = false
		return core.Snapshot{Revision: 1}, nil
	}
	return core.Snapshot{}, s.err
}

func (s *errorSnapshotStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

type scriptedSnapshotStream struct {
	snapshots chan core.Snapshot
	closed    chan struct{}
	closeOnce sync.Once
}

func newScriptedSnapshotStream(snapshots ...core.Snapshot) *scriptedSnapshotStream {
	stream := &scriptedSnapshotStream{
		snapshots: make(chan core.Snapshot, len(snapshots)),
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

const (
	helloWithWatch = `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"]}}`
	watchRequest   = `{"jsonrpc":"2.0","id":"2","method":"manager.watch","params":{"scope":{"kind":"all"}}}`
)

func startWatchSession(t *testing.T, manager core.Manager) *testSession {
	t.Helper()
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, helloWithWatch)
	session.exchange(t, watchRequest)
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

func TestServeDoesNotNotifyAfterUnwatchResponse(t *testing.T) {
	stream := newStaleResultSnapshotStream()
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          stream,
	}
	session := startWatchSession(t, manager)
	select {
	case <-stream.waiting:
	case <-time.After(time.Second):
		t.Fatal("Watch worker did not request its next Snapshot")
	}
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"3","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	want := `{"jsonrpc":"2.0","id":"3","result":{"watch_id":"watch-1","stopped":true}}`
	assertJSONEqual(t, response, []byte(want))
	close(stream.release)

	assertNoFrameWithin(t, session, "notification followed unwatch response")
}

func TestServeForwardsManagerResyncRequirement(t *testing.T) {
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          newErrorSnapshotStream(core.ErrResyncRequired),
	}
	session := startWatchSession(t, manager)
	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read resync notification: %v", err)
	}
	want := `{"jsonrpc":"2.0","method":"manager.resync_required","params":{"watch_id":"watch-1","reason":"manager_resync_required"}}`
	assertJSONEqual(t, notification, []byte(want))
}

func TestServeEndsWatchSilentlyOnStreamCloseError(t *testing.T) {
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          newErrorSnapshotStream(core.ErrSnapshotStreamClosed),
	}
	session := startWatchSession(t, manager)
	assertNoFrameWithin(t, session, "stream close emitted a frame")

	response := session.exchange(t, `{"jsonrpc":"2.0","id":"3","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	want := `{"jsonrpc":"2.0","id":"3","result":{"watch_id":"watch-1","stopped":false}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestSnapshotNotificationDoesNotReleaseRequestAdmission(t *testing.T) {
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
	manager := &watchBlockingManager{
		blockingManager: &blockingManager{started: make(chan struct{}, 8)},
		stream:          stream,
	}
	session := startWatchSession(t, manager)

	var requests bytes.Buffer
	padding := strings.Repeat("x", 16<<10)
	for id := 3; id < 68; id++ {
		fmt.Fprintf(&requests, `{"jsonrpc":"2.0","id":%d,"method":"manager.snapshot","params":{"scope":{"kind":"all","padding":"%s"}}}`, id, padding)
		requests.WriteByte('\n')
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := session.client.Write(requests.Bytes())
		writeDone <- err
	}()
	for range 8 {
		select {
		case <-manager.started:
		case <-time.After(time.Second):
			t.Fatal("fewer than eight Snapshot handlers started")
		}
	}
	stream.snapshots <- core.Snapshot{Revision: 2}
	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read Snapshot notification: %v", err)
	}
	want := `{"jsonrpc":"2.0","method":"manager.snapshot","params":{"watch_id":"watch-1","snapshot":{"revision":2}}}`
	assertJSONEqual(t, notification, []byte(want))
	select {
	case err := <-writeDone:
		t.Fatalf("notification released request admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	session.cancel()
	if err := session.wait(); err != nil {
		t.Fatalf("cancel saturated session: %v", err)
	}
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("blocked request writer did not stop")
	}
}

func TestServeRequestsResyncInsteadOfSendingOversizedSnapshot(t *testing.T) {
	oversized := core.Snapshot{
		Revision: 2,
		Host: &core.HostSnapshot{
			Alias: core.HostAlias(strings.Repeat("x", 1<<20)),
		},
	}
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: 1}, oversized)
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          stream,
	}
	session := startWatchSession(t, manager)
	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read resync notification: %v", err)
	}
	assertJSONEqual(t, notification, protocolFixture(t, "watch-resync-required.jsonl"))
}

func TestServeClosesWatchesWhenConnectionEnds(t *testing.T) {
	manager := &multiWatchManager{snapshotManager: &snapshotManager{}}
	session := startWatchSession(t, manager)
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

func TestServeBoundsActiveWatchesPerConnection(t *testing.T) {
	manager := &multiWatchManager{snapshotManager: &snapshotManager{}}
	session := startWatchSession(t, manager)
	for index := 2; index <= 8; index++ {
		request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"manager.watch","params":{"scope":{"kind":"all"}}}`, index+1)
		response := session.exchange(t, request)
		want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"watch_id":"watch-%d","snapshot":{"revision":%d}}}`, index+1, index, index)
		assertJSONEqual(t, response, []byte(want))
	}
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"limit","method":"manager.watch","params":{"scope":{"kind":"all"}}}`)
	want := `{"jsonrpc":"2.0","id":"limit","error":{"code":-32015,"message":"too many active Watches","data":{"kind":"watch_limit","retryable":true}}}`
	assertJSONEqual(t, response, []byte(want))

	session.exchange(t, `{"jsonrpc":"2.0","id":"stop","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	response = session.exchange(t, `{"jsonrpc":"2.0","id":"replacement","method":"manager.watch","params":{"scope":{"kind":"all"}}}`)
	want = `{"jsonrpc":"2.0","id":"replacement","result":{"watch_id":"watch-9","snapshot":{"revision":9}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeRequiresNegotiatedCapabilityForWatchMethods(t *testing.T) {
	for _, request := range []string{
		`{"jsonrpc":"2.0","id":"2","method":"manager.watch","params":{"scope":{"kind":"all"}}}`,
		`{"jsonrpc":"2.0","id":"2","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`,
	} {
		session := newTestSession(t)
		session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
		response := session.exchange(t, request)
		want := `{"jsonrpc":"2.0","id":"2","error":{"code":-32003,"message":"watch-snapshot-v1 capability is required","data":{"kind":"capability_required","retryable":false}}}`
		assertJSONEqual(t, response, []byte(want))
	}
}

func TestServeUnwatchIsIdempotentAndStopsStream(t *testing.T) {
	stream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          stream,
	}
	session := startWatchSession(t, manager)

	response := session.exchange(t, `{"jsonrpc":"2.0","id":"3","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	want := `{"jsonrpc":"2.0","id":"3","result":{"watch_id":"watch-1","stopped":true}}`
	assertJSONEqual(t, response, []byte(want))
	response = session.exchange(t, `{"jsonrpc":"2.0","id":"4","method":"manager.unwatch","params":{"watch_id":"watch-1"}}`)
	want = `{"jsonrpc":"2.0","id":"4","result":{"watch_id":"watch-1","stopped":false}}`
	assertJSONEqual(t, response, []byte(want))

	if _, err := stream.Next(context.Background()); !errors.Is(err, core.ErrSnapshotStreamClosed) {
		t.Fatalf("Snapshot stream after unwatch = %v, want closed", err)
	}
}

func TestServeWatchResponsePrecedesSnapshotNotification(t *testing.T) {
	stream := newScriptedSnapshotStream(
		core.Snapshot{Revision: 3},
		core.Snapshot{Revision: 4},
	)
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          stream,
	}
	session := newTestSessionWithManager(t, manager)
	hello := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"]}}`)
	wantHello := `{"jsonrpc":"2.0","id":"1","result":{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"],"max_frame_bytes":1048576}}`
	assertJSONEqual(t, hello, []byte(wantHello))

	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.watch","params":{"scope":{"kind":"all"}}}`)
	wantResponse := `{"jsonrpc":"2.0","id":"2","result":{"watch_id":"watch-1","snapshot":{"revision":3}}}`
	assertJSONEqual(t, response, []byte(wantResponse))

	notification, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read Snapshot notification: %v", err)
	}
	assertJSONEqual(t, notification, protocolFixture(t, "watch-notification.jsonl"))

	if err := stream.Close(); err != nil && !errors.Is(err, core.ErrSnapshotStreamClosed) {
		t.Fatalf("close Snapshot stream: %v", err)
	}
}

func protocolFixture(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "protocol", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(string(contents)))
}
