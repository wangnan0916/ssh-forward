package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/creachadair/jrpc2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// DialConn checks the protocol version and returns a Manager whose operations
// are remote JSON-RPC calls.
func DialConn(ctx context.Context, conn net.Conn) (core.Manager, error) {
	frames := newFrameChannel(conn, maxFrameBytes)
	client := &managerClient{
		watches: make(map[string]*remoteStream),
		pending: make(map[string]watchUpdate),
	}
	client.rpc = jrpc2.NewClient(frames, &jrpc2.ClientOptions{
		OnNotify: client.onNotify,
		OnStop: func(_ *jrpc2.Client, err error) {
			client.failWatches(err)
		},
	})
	var version versionResult
	if err := client.call(ctx, methodVersion, nil, &version); err != nil {
		_ = client.rpc.Close()
		return nil, fmt.Errorf("manager protocol version: %w", err)
	}
	if version.Version != protocolVersion {
		_ = client.rpc.Close()
		return nil, fmt.Errorf("manager speaks protocol %d, want %d", version.Version, protocolVersion)
	}
	return client, nil
}

// managerClient implements core.Manager over the wire through jrpc2.
type managerClient struct {
	rpc *jrpc2.Client

	mu      sync.Mutex
	watches map[string]*remoteStream
	pending map[string]watchUpdate
	closed  bool
}

type watchUpdate struct {
	snapshot *core.Snapshot
	err      error
}

func (u watchUpdate) apply(stream *remoteStream) {
	if u.err != nil {
		stream.fail(u.err)
		return
	}
	if u.snapshot != nil {
		stream.push(*u.snapshot)
	}
}

func (c *managerClient) call(ctx context.Context, method string, params, result any) error {
	return decodeRPCError(c.rpc.CallResult(ctx, method, params, result))
}

func (c *managerClient) Snapshot(ctx context.Context) (core.Snapshot, error) {
	var wrapped snapshotResult
	if err := c.call(ctx, methodSnapshot, nil, &wrapped); err != nil {
		return core.Snapshot{}, err
	}
	return wrapped.Snapshot, nil
}

func (c *managerClient) Watch(ctx context.Context) (core.SnapshotStream, error) {
	var payload watchResult
	if err := c.call(ctx, methodWatch, nil, &payload); err != nil {
		return nil, err
	}
	stream := newRemoteStream(c, payload.WatchID, payload.Snapshot)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("manager connection is closed")
	}
	c.watches[payload.WatchID] = stream
	pending, hasPending := c.pending[payload.WatchID]
	delete(c.pending, payload.WatchID)
	c.mu.Unlock()
	if hasPending {
		pending.apply(stream)
	}
	return stream, nil
}

func (c *managerClient) Close(context.Context) error {
	c.failWatches(errors.New("manager connection closed"))
	return c.rpc.Close()
}

func (c *managerClient) onNotify(request *jrpc2.Request) {
	switch request.Method() {
	case methodSnapshot:
		var payload snapshotNotification
		if request.UnmarshalParams(&payload) != nil {
			return
		}
		c.deliver(payload.WatchID, watchUpdate{snapshot: &payload.Snapshot})
	case methodResyncRequired:
		var payload resyncNotification
		if request.UnmarshalParams(&payload) != nil {
			return
		}
		c.deliver(payload.WatchID, watchUpdate{err: core.ErrResyncRequired})
	}
}

func (c *managerClient) deliver(watchID string, update watchUpdate) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	stream := c.watches[watchID]
	if stream == nil {
		c.pending[watchID] = update
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	update.apply(stream)
}

func (c *managerClient) failWatches(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for id, stream := range c.watches {
		stream.fail(err)
		delete(c.watches, id)
	}
	clear(c.pending)
}

func (c *managerClient) unwatch(watchID string) {
	_ = c.call(context.Background(), methodUnwatch, unwatchParams{WatchID: watchID}, new(unwatchResult))
}

// remoteStream is one Watch over the wire. Unread notifications coalesce
// to the latest Snapshot. Close-before-Next still returns the subscribe
// Snapshot; core's in-process stream does not (TakeWatchSnapshot closedFirst).
type remoteStream struct {
	client  *managerClient
	watchID string

	mu             sync.Mutex
	initial        core.Snapshot
	initialPending bool
	latest         *core.Snapshot
	nextActive     bool
	ready          chan struct{}
	failed         error
}

func newRemoteStream(client *managerClient, watchID string, initial core.Snapshot) *remoteStream {
	return &remoteStream{
		client:         client,
		watchID:        watchID,
		initial:        initial,
		initialPending: true,
		ready:          make(chan struct{}, 1),
	}
}

func (s *remoteStream) push(snapshot core.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return
	}
	s.latest = &snapshot
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *remoteStream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = err
		select {
		case s.ready <- struct{}{}:
		default:
		}
	}
}

func (s *remoteStream) Next(ctx context.Context) (core.Snapshot, error) {
	return core.AwaitWatch(ctx, &s.mu, s.ready, &s.nextActive, s.nextLocked)
}

func (s *remoteStream) nextLocked() (core.Snapshot, bool, error) {
	return core.TakeWatchSnapshot(false, s.failed != nil, s.failed, &s.initialPending, s.initial, &s.latest)
}

func (s *remoteStream) Close() error {
	s.client.mu.Lock()
	registered := s.client.watches[s.watchID] == s
	s.client.mu.Unlock()
	if registered {
		s.fail(errors.New("watch closed"))
		s.client.unwatch(s.watchID)
		s.client.mu.Lock()
		if s.client.watches[s.watchID] == s {
			delete(s.client.watches, s.watchID)
		}
		delete(s.client.pending, s.watchID)
		s.client.mu.Unlock()
	}
	return nil
}

func decodeRPCError(err error) error {
	if err == nil {
		return nil
	}
	var rpcErr *jrpc2.Error
	if !errors.As(err, &rpcErr) {
		return err
	}
	var data errorData
	if json.Unmarshal(rpcErr.Data, &data) != nil || data.Kind == "" {
		return err
	}
	return &core.DomainError{Kind: core.ErrorKind(data.Kind), Retryable: data.Retryable}
}
