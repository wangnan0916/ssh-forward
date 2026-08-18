package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/creachadair/jrpc2/channel"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

// Dial negotiates a JSON-RPC v1 session on conn and returns a Manager
// whose operations are remote calls. The returned Manager owns conn.
func Dial(ctx context.Context, conn net.Conn) (core.Manager, error) {
	frames := &serializedChannel{Channel: newBoundedLineChannel(conn, maxFrameBytes)}
	client := &managerClient{
		frames:  frames,
		pending: make(map[uint64]chan wireResponse),
		watches: make(map[string]*socketStream),
	}
	client.startReadLoop()
	if err := client.hello(ctx); err != nil {
		_ = frames.Close()
		return nil, err
	}
	return client, nil
}

// managerClient implements core.Manager over the wire: Snapshot is a
// request/response call correlated by ID, Watch subscribes to server
// notifications, and Close tears the connection down.
type managerClient struct {
	frames channel.Channel

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan wireResponse
	watches map[string]*socketStream
	closed  bool

	readErr error
}

type wireResponse struct {
	ID     json.RawMessage
	Result json.RawMessage
	Error  *wireError
}

func (c *managerClient) hello(ctx context.Context) error {
	params := []byte(`{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"]}`)
	result, err := c.call(ctx, "system.hello", params, 1)
	if err != nil {
		return fmt.Errorf("manager hello: %w", err)
	}
	var hello helloResult
	if err := json.Unmarshal(result, &hello); err != nil {
		return fmt.Errorf("manager hello: malformed result: %w", err)
	}
	if hello.Protocol.Major != protocolMajor {
		return fmt.Errorf("manager speaks protocol %d, want %d", hello.Protocol.Major, protocolMajor)
	}
	return nil
}

func (c *managerClient) call(ctx context.Context, method string, params []byte, id uint64) (json.RawMessage, error) {
	response := c.beginCall(id)
	if err := c.write(requestEnvelope{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf(`%d`, id)),
		Method:  method,
		Params:  params,
	}); err != nil {
		c.finishCall(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case reply := <-response:
		if reply.Error != nil {
			return nil, decodeServerError(reply.Error)
		}
		return reply.Result, nil
	}
}

func (c *managerClient) beginCall(id uint64) chan wireResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	response := make(chan wireResponse, 1)
	c.pending[id] = response
	return response
}

func (c *managerClient) finishCall(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, id)
}

func (c *managerClient) newID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *managerClient) write(request requestEnvelope) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return c.frames.Send(encoded)
}

func (c *managerClient) startReadLoop() {
	go func() {
		for {
			line, err := c.frames.Recv()
			if err != nil {
				c.failAll(err)
				return
			}
			c.dispatch(line)
		}
	}()
}

func (c *managerClient) dispatch(line []byte) {
	shape, ok := decodeEnvelopeShape(line)
	if !ok {
		return
	}
	if shape.HasID && !bytes.Equal(bytes.TrimSpace(shape.ID), []byte("null")) && shape.Method == nil {
		var id uint64
		if json.Unmarshal(shape.ID, &id) != nil {
			return
		}
		reply := wireResponse{ID: shape.ID, Result: shape.Result}
		if len(shape.Error) > 0 && !bytes.Equal(bytes.TrimSpace(shape.Error), []byte("null")) {
			var parsed wireError
			if json.Unmarshal(shape.Error, &parsed) != nil {
				return
			}
			reply.Error = &parsed
		}
		c.mu.Lock()
		waiting, found := c.pending[id]
		if found {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if found {
			waiting <- reply
		}
		return
	}
	var method string
	if json.Unmarshal(shape.Method, &method) != nil || method != methodSnapshot {
		return
	}
	var payload struct {
		WatchID  string          `json:"watch_id"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(shape.Params, &payload) != nil {
		return
	}
	c.mu.Lock()
	stream := c.watches[payload.WatchID]
	c.mu.Unlock()
	if stream != nil {
		stream.push(payload.Snapshot)
	}
}

func (c *managerClient) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.readErr = err
	for id, waiting := range c.pending {
		waiting <- wireResponse{Error: &wireError{Message: err.Error()}}
		delete(c.pending, id)
	}
	for id, stream := range c.watches {
		stream.fail(err)
		delete(c.watches, id)
	}
}

func (c *managerClient) Snapshot(ctx context.Context) (core.Snapshot, error) {
	result, err := c.call(ctx, methodSnapshot, []byte(`{}`), c.newID())
	if err != nil {
		return core.Snapshot{}, err
	}
	var wrapped struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(result, &wrapped); err != nil {
		return core.Snapshot{}, fmt.Errorf("snapshot result: %w", err)
	}
	return snapshot.Unmarshal(wrapped.Snapshot)
}

func (c *managerClient) Watch(ctx context.Context) (core.SnapshotStream, error) {
	result, err := c.call(ctx, methodWatch, []byte(`{}`), c.newID())
	if err != nil {
		return nil, err
	}
	var payload struct {
		WatchID  string          `json:"watch_id"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("watch result: %w", err)
	}
	stream := newSocketStream(c, payload.WatchID, payload.Snapshot)
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
	c.mu.Lock()
	closed := c.closed
	c.closed = true
	c.mu.Unlock()
	if !closed {
		for id, stream := range c.watches {
			stream.fail(errors.New("manager connection closed"))
			delete(c.watches, id)
		}
	}
	return c.frames.Close()
}

// socketStream is one Watch over the wire. Unread notifications coalesce
// to the latest value, matching the Manager stream. The latest-value loop
// lives here and in core's snapshotStream rather than a shared helper
// because the two shapes differ: core holds Snapshot values; the client
// holds json.RawMessage until Unmarshal.
type socketStream struct {
	client  *managerClient
	watchID string

	mu             sync.Mutex
	initial        json.RawMessage
	initialPending bool
	latest         json.RawMessage
	nextActive     bool
	ready          chan struct{}
	failed         error
}

func newSocketStream(client *managerClient, watchID string, initial json.RawMessage) *socketStream {
	return &socketStream{
		client:         client,
		watchID:        watchID,
		initial:        initial,
		initialPending: true,
		ready:          make(chan struct{}, 1),
	}
}

func (s *socketStream) push(snapshot json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return
	}
	s.latest = snapshot
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *socketStream) fail(err error) {
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

func (s *socketStream) Next(ctx context.Context) (core.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return core.Snapshot{}, err
	}
	if !s.beginNext() {
		return core.Snapshot{}, core.ErrConcurrentSnapshotNext
	}
	defer s.endNext()
	for {
		s.mu.Lock()
		payload, found, err := s.nextLocked()
		s.mu.Unlock()
		if err != nil {
			return core.Snapshot{}, err
		}
		if found {
			return snapshot.Unmarshal(payload)
		}
		select {
		case <-ctx.Done():
			return core.Snapshot{}, ctx.Err()
		case <-s.ready:
		}
	}
}

func (s *socketStream) nextLocked() (json.RawMessage, bool, error) {
	if s.initialPending {
		s.initialPending = false
		return s.initial, true, nil
	}
	if len(s.latest) > 0 {
		payload := s.latest
		s.latest = nil
		return payload, true, nil
	}
	if s.failed != nil {
		return nil, false, s.failed
	}
	return nil, false, nil
}

func (s *socketStream) beginNext() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextActive {
		return false
	}
	s.nextActive = true
	return true
}

func (s *socketStream) endNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextActive = false
}

func (s *socketStream) Close() error {
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

func (c *managerClient) unwatch(watchID string) {
	params := []byte(fmt.Sprintf(`{"watch_id":%q}`, watchID))
	_, _ = c.call(context.Background(), methodUnwatch, params, c.newID())
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
