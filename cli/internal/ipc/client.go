package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"ssh-forward/cli/internal/core"
	managerjsonrpc "ssh-forward/cli/internal/jsonrpc"
)

// maxFrameBytes mirrors the server's bound: any line longer than this ends
// the connection (hostile or broken peers cannot grow client memory).
const maxFrameBytes = 1_048_576

// Dial connects to the running singleton and negotiates the JSON-RPC v1
// session, returning a Manager whose operations are remote calls. The
// returned Manager is single-use: one process one session, matching the
// CLI's per-command lifetime.
func Dial(ctx context.Context, path string) (core.Manager, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	client := &managerClient{
		conn:    conn,
		reader:  bufio.NewReaderSize(conn, 64*1024),
		pending: make(map[uint64]chan wireResponse),
		watches: make(map[string]*socketStream),
	}
	// The read loop must run before the hello call: its response arrives
	// through the same frame dispatcher as every other call.
	client.startReadLoop()
	if err := client.hello(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

// managerClient implements core.Manager over the IPC wire: Execute and
// Snapshot are request/response calls correlated by ID, Watch subscribes
// to server notifications, and Close tears the connection down.
//
// The read loop runs in one goroutine and dispatches every frame: matching
// responses wake their pending call, notifications (manager.snapshot) push
// into their watch stream, and a broken connection fails everything.
type managerClient struct {
	conn   net.Conn
	reader *bufio.Reader

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan wireResponse
	watches map[string]*socketStream
	closed  bool

	readErr error
}

type wireResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireErrorShape `json:"error,omitempty"`
}

type wireErrorShape struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type wireNotification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (c *managerClient) hello(ctx context.Context) error {
	params := []byte(`{"protocol":{"major":1,"minor":0},"capabilities":["cancel-v1","watch-snapshot-v1"]}`)
	result, err := c.call(ctx, "system.hello", params, 1)
	if err != nil {
		return fmt.Errorf("manager hello: %w", err)
	}
	var hello struct {
		Protocol struct {
			Major int `json:"major"`
		} `json:"protocol"`
		MaxFrameBytes int `json:"max_frame_bytes"`
	}
	if err := json.Unmarshal(result, &hello); err != nil {
		return fmt.Errorf("manager hello: malformed result: %w", err)
	}
	if hello.Protocol.Major != 1 {
		return fmt.Errorf("manager speaks protocol %d, want 1", hello.Protocol.Major)
	}
	return nil
}

// call sends one request and waits for its correlated response.
func (c *managerClient) call(ctx context.Context, method string, params []byte, id uint64) (json.RawMessage, error) {
	response := c.beginCall(id)
	if err := c.write(wireRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf(`%d`, id)), Method: method, Params: params}); err != nil {
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

func (c *managerClient) write(request wireRequest) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(encoded) > maxFrameBytes || len(encoded)+1 > maxFrameBytes {
		return errors.New("outbound frame exceeds the protocol bound")
	}
	if _, err := c.conn.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

// startReadLoop dispatches every inbound frame until the connection ends.
func (c *managerClient) startReadLoop() {
	go func() {
		for {
			line, err := c.readFrame()
			if err != nil {
				c.failAll(err)
				return
			}
			c.dispatch(line)
		}
	}()
}

func (c *managerClient) readFrame() ([]byte, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	if len(line) > maxFrameBytes {
		return nil, errors.New("inbound frame exceeds the protocol bound")
	}
	return line, nil
}

func (c *managerClient) dispatch(line []byte) {
	var shape struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(line, &shape); err != nil {
		return
	}
	if len(shape.ID) > 0 && string(shape.ID) != "null" {
		var response wireResponse
		if json.Unmarshal(line, &response) != nil {
			return
		}
		var id uint64
		if json.Unmarshal(shape.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		waiting, found := c.pending[id]
		if found {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if found {
			waiting <- response
		}
		return
	}
	// Notification: manager.snapshot carries a watch_id.
	var notification wireNotification
	if shape.Method == "manager.snapshot" && json.Unmarshal(line, &notification) == nil {
		var payload struct {
			WatchID  string          `json:"watch_id"`
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if json.Unmarshal(notification.Params, &payload) == nil {
			c.mu.Lock()
			stream := c.watches[payload.WatchID]
			c.mu.Unlock()
			if stream != nil {
				stream.push(payload.Snapshot)
			}
		}
	}
}

// failAll ends every pending call and watch with the connection error.
func (c *managerClient) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.readErr = err
	for id, waiting := range c.pending {
		waiting <- wireResponse{Error: &wireErrorShape{Message: err.Error()}}
		delete(c.pending, id)
	}
	for id, stream := range c.watches {
		stream.fail(err)
		delete(c.watches, id)
	}
}

// Execute implements core.Manager over the wire.
func (c *managerClient) Execute(ctx context.Context, command core.Command) (core.Outcome, error) {
	wireCommand, err := managerjsonrpc.MarshalCommand(command)
	if err != nil {
		return core.Outcome{}, err
	}
	params := map[string]json.RawMessage{"command": wireCommand}
	encoded, err := json.Marshal(params)
	if err != nil {
		return core.Outcome{}, err
	}
	result, err := c.call(ctx, "manager.execute", encoded, c.newID())
	if err != nil {
		return core.Outcome{}, err
	}
	var wrapped struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := json.Unmarshal(result, &wrapped); err != nil {
		return core.Outcome{}, fmt.Errorf("execute result: %w", err)
	}
	return managerjsonrpc.UnmarshalOutcome(wrapped.Outcome)
}

// Snapshot implements core.Manager over the wire.
func (c *managerClient) Snapshot(ctx context.Context) (core.Snapshot, error) {
	result, err := c.call(ctx, "manager.snapshot", []byte(`{"scope":{"kind":"all"}}`), c.newID())
	if err != nil {
		return core.Snapshot{}, err
	}
	var wrapped struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(result, &wrapped); err != nil {
		return core.Snapshot{}, fmt.Errorf("snapshot result: %w", err)
	}
	return managerjsonrpc.UnmarshalSnapshot(wrapped.Snapshot)
}

// Watch implements core.Manager over the wire: the response snapshot is
// queued, then each manager.snapshot notification appends one.
func (c *managerClient) Watch(ctx context.Context) (core.SnapshotStream, error) {
	result, err := c.call(ctx, "manager.watch", []byte(`{"scope":{"kind":"all"}}`), c.newID())
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
	stream := newSocketStream(c, payload.WatchID)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("manager connection is closed")
	}
	c.watches[payload.WatchID] = stream
	c.mu.Unlock()
	stream.push(payload.Snapshot)
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
	return c.conn.Close()
}

// socketStream is one Watch over the wire: a bounded queue of snapshots.
type socketStream struct {
	client  *managerClient
	watchID string

	mu      sync.Mutex
	queue   []json.RawMessage
	ready   chan struct{}
	failed  error
	unwatch bool
}

func newSocketStream(client *managerClient, watchID string) *socketStream {
	return &socketStream{client: client, watchID: watchID, ready: make(chan struct{}, 1)}
}

func (s *socketStream) push(snapshot json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return
	}
	s.queue = append(s.queue, snapshot)
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

// Next implements core.SnapshotStream.
func (s *socketStream) Next(ctx context.Context) (core.Snapshot, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			snapshot := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return managerjsonrpc.UnmarshalSnapshot(snapshot)
		}
		if s.failed != nil {
			err := s.failed
			s.mu.Unlock()
			return core.Snapshot{}, err
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return core.Snapshot{}, ctx.Err()
		case <-s.ready:
		}
	}
}

// Close implements core.SnapshotStream: idempotently unwatch and drop the
// stream.
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
	// Best-effort: the caller is tearing down anyway.
	_, _ = c.call(context.Background(), "manager.unwatch", params, c.newID())
}

// decodeServerError maps a wire error back to the domain error shape or a
// plain error for protocol-level failures.
func decodeServerError(wire *wireErrorShape) error {
	if len(wire.Data) > 0 {
		var data struct {
			Kind      string `json:"kind"`
			Retryable bool   `json:"retryable"`
		}
		if json.Unmarshal(wire.Data, &data) == nil && data.Kind != "" {
			return &core.DomainError{Kind: core.ErrorKind(data.Kind), Retryable: data.Retryable}
		}
	}
	return errors.New(wire.Message)
}
