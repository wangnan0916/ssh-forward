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
	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

// DialConn negotiates a JSON-RPC v1 session on conn and returns a Manager
// whose operations are remote calls. Hello stays outside jrpc2 (same as
// ServeConn); afterwards Dial and Serve share the jrpc2 session.
func DialConn(ctx context.Context, conn net.Conn) (core.Manager, error) {
	frames := newFrameChannel(conn, maxFrameBytes)
	stop := context.AfterFunc(ctx, func() { _ = frames.Close() })
	if err := offerHello(frames); err != nil {
		stop()
		_ = frames.Close()
		return nil, err
	}
	if !stop() || ctx.Err() != nil {
		_ = frames.Close()
		return nil, ctx.Err()
	}

	client := &managerClient{watches: make(map[string]*remoteStream)}
	client.rpc = jrpc2.NewClient(frames, &jrpc2.ClientOptions{
		OnNotify: client.onNotify,
		OnStop: func(_ *jrpc2.Client, err error) {
			client.failWatches(err)
		},
	})
	return client, nil
}

func offerHello(frames *frameChannel) error {
	if err := sendEnvelope(frames, requestEnvelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "system.hello",
		Params:  json.RawMessage(`{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"]}`),
	}); err != nil {
		return fmt.Errorf("manager hello: %w", err)
	}
	message, err := frames.Recv()
	if err != nil {
		return fmt.Errorf("manager hello: %w", err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *wireError      `json:"error"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return fmt.Errorf("manager hello: malformed result: %w", err)
	}
	if envelope.Error != nil {
		return decodeServerError(envelope.Error)
	}
	var hello helloResult
	if err := json.Unmarshal(envelope.Result, &hello); err != nil {
		return fmt.Errorf("manager hello: malformed result: %w", err)
	}
	if hello.Protocol.Major != protocolMajor {
		return fmt.Errorf("manager speaks protocol %d, want %d", hello.Protocol.Major, protocolMajor)
	}
	return nil
}

// managerClient implements core.Manager over the wire through jrpc2.
type managerClient struct {
	rpc *jrpc2.Client

	mu      sync.Mutex
	watches map[string]*remoteStream
	closed  bool
}

func (c *managerClient) call(ctx context.Context, method string, params, result any) error {
	return decodeRPCError(c.rpc.CallResult(ctx, method, params, result))
}

func (c *managerClient) Snapshot(ctx context.Context) (core.Snapshot, error) {
	var wrapped snapshotResult
	if err := c.call(ctx, methodSnapshot, struct{}{}, &wrapped); err != nil {
		return core.Snapshot{}, err
	}
	return snapshot.Decode(wrapped.Snapshot), nil
}

func (c *managerClient) Watch(ctx context.Context) (core.SnapshotStream, error) {
	var payload watchResult
	if err := c.call(ctx, methodWatch, struct{}{}, &payload); err != nil {
		return nil, err
	}
	stream := newRemoteStream(c, payload.WatchID, snapshot.Decode(payload.Snapshot))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("manager connection is closed")
	}
	c.watches[payload.WatchID] = stream
	c.mu.Unlock()
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
		if stream := c.lookup(payload.WatchID); stream != nil {
			stream.push(snapshot.Decode(payload.Snapshot))
		}
	case methodResyncRequired:
		var payload resyncNotification
		if request.UnmarshalParams(&payload) != nil {
			return
		}
		if stream := c.lookup(payload.WatchID); stream != nil {
			stream.fail(core.ErrResyncRequired)
		}
	}
}

func (c *managerClient) lookup(watchID string) *remoteStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watches[watchID]
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
	_, registered := s.client.watches[s.watchID]
	delete(s.client.watches, s.watchID)
	s.client.mu.Unlock()
	if registered {
		s.fail(errors.New("watch closed"))
		s.client.unwatch(s.watchID)
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

func decodeServerError(wire *wireError) error {
	if wire == nil {
		return errors.New("empty error")
	}
	if data, ok := wire.Data.(map[string]any); ok {
		kind, _ := data["kind"].(string)
		retryable, _ := data["retryable"].(bool)
		if kind != "" {
			return &core.DomainError{Kind: core.ErrorKind(kind), Retryable: retryable}
		}
	}
	return errors.New(wire.Message)
}
