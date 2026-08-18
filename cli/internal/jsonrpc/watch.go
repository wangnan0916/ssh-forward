package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/creachadair/jrpc2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

type connectionSession struct {
	ctx          context.Context
	cancel       context.CancelFunc
	manager      core.Manager
	capabilities negotiatedCapabilities
	server       *jrpc2.Server

	mu             sync.Mutex
	workers        sync.WaitGroup
	closed         bool
	pendingWatches int
	nextWatchID    uint64
	watches        map[string]*connectionWatch
	// pendingWatchResponses maps in-flight manager.watch request ids to the
	// server-assigned Watch IDs their responses will introduce. onResponseSent
	// consumes one entry per delivered response, so the map is bounded by the
	// number of watch requests in flight (itself bounded by the watch slots)
	// and dies with the session.
	pendingWatchResponses map[string]string
}

type connectionWatch struct {
	id       string
	stream   core.SnapshotStream
	activate chan struct{}

	mu      sync.Mutex
	stopped bool
}

func newConnectionSession(ctx context.Context, manager core.Manager, capabilities negotiatedCapabilities) *connectionSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	return &connectionSession{
		ctx:                   sessionCtx,
		cancel:                cancel,
		manager:               manager,
		capabilities:          capabilities,
		watches:               make(map[string]*connectionWatch),
		pendingWatchResponses: make(map[string]string),
	}
}

func (s *connectionSession) handleWatch(ctx context.Context, request *jrpc2.Request) (any, error) {
	if !s.capabilities.watchSnapshot {
		return nil, errWatchCapabilityRequired
	}
	if err := parseSnapshotParams(request); err != nil {
		return nil, err
	}
	if !s.reserveWatchSlot() {
		return nil, errWatchLimit
	}
	stream, err := s.manager.Watch(ctx)
	if err != nil {
		s.releaseWatchSlot()
		return nil, marshalManagerError(err)
	}
	initial, err := stream.Next(ctx)
	if err != nil {
		s.releaseWatchSlot()
		_ = stream.Close()
		return nil, internalError()
	}
	watch, ok := s.registerWatch(stream)
	if !ok {
		s.releaseWatchSlot()
		_ = stream.Close()
		return nil, internalError()
	}
	// Record which request id this response introduces: onResponseSent then
	// activates the Watch by looking up the already-decoded request id,
	// instead of re-parsing the result the handler itself just marshalled.
	s.mu.Lock()
	s.pendingWatchResponses[request.ID()] = watch.id
	s.mu.Unlock()

	go func() {
		defer s.workers.Done()
		s.runWatch(watch)
	}()
	return watchResult{
		WatchID:  watch.id,
		Snapshot: snapshot.Encode(initial),
	}, nil
}

// onResponseSent runs after a response frame is written, so no notification
// can overtake the response that introduces a Watch. handleWatch recorded
// the request-id → watch-id mapping before the response was written, and the
// channel delivers the already-decoded request id here, so no result parsing
// is needed; the lookup always succeeds for watch responses. The guard keeps
// the activation channel closed at most once.
func (s *connectionSession) onResponseSent(envelope decodedResponse) {
	s.mu.Lock()
	watchID, ok := s.pendingWatchResponses[string(envelope.ID)]
	delete(s.pendingWatchResponses, string(envelope.ID))
	s.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	watch := s.watches[watchID]
	s.mu.Unlock()
	if watch == nil {
		return
	}
	select {
	case <-watch.activate:
	default:
		close(watch.activate)
	}
}

func (s *connectionSession) handleUnwatch(_ context.Context, request *jrpc2.Request) (any, error) {
	if !s.capabilities.watchSnapshot {
		return nil, errWatchCapabilityRequired
	}
	var params unwatchParams
	if paramsText := request.ParamString(); paramsText == "" || json.Unmarshal([]byte(paramsText), &params) != nil ||
		len(params.WatchID) == 0 || len(params.WatchID) > maxWatchID {
		return nil, errInvalidParameters
	}
	stopped := s.removeWatch(params.WatchID, nil)
	return unwatchResult{WatchID: params.WatchID, Stopped: stopped}, nil
}

func (s *connectionSession) reserveWatchSlot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.watches)+s.pendingWatches >= maxSessionWatches {
		return false
	}
	s.pendingWatches++
	return true
}

func (s *connectionSession) releaseWatchSlot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingWatches--
}

// registerWatch converts a reserved slot into a registered Watch and assigns
// its ID, all under the session lock; it fails only if the session closed
// while the stream was being set up. The worker count is added under the
// same lock: close() sets closed there and only then runs Wait, so a new
// Add can never follow Wait (WaitGroup misuse).
func (s *connectionSession) registerWatch(stream core.SnapshotStream) (*connectionWatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	s.pendingWatches--
	s.nextWatchID++
	watch := &connectionWatch{
		id:       "watch-" + strconv.FormatUint(s.nextWatchID, 10),
		stream:   stream,
		activate: make(chan struct{}),
	}
	s.watches[watch.id] = watch
	s.workers.Add(1)
	return watch, true
}

func (s *connectionSession) runWatch(watch *connectionWatch) {
	defer s.removeWatch(watch.id, watch)
	select {
	case <-watch.activate:
	case <-s.ctx.Done():
		return
	}
	for {
		snap, err := watch.stream.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil && errors.Is(err, core.ErrResyncRequired) {
				s.sendResyncRequired(watch, "manager_resync_required")
			}
			return
		}
		params := snapshotNotification{WatchID: watch.id, Snapshot: snapshot.Encode(snap)}
		if !notificationFits(methodSnapshot, params) {
			s.sendResyncRequired(watch, "snapshot_too_large")
			return
		}
		if !s.sendWatchNotification(watch, methodSnapshot, params) {
			return
		}
	}
}

// sendResyncRequired ends the Watch with a resync notification. The payload
// is bounded (watch id plus a literal reason), so it always fits the frame
// limit and needs no size gate; if the write itself fails, the connection is
// broken and the client reconnects for a fresh complete Snapshot, as the
// protocol doc promises.
func (s *connectionSession) sendResyncRequired(watch *connectionWatch, reason string) {
	params := resyncNotification{WatchID: watch.id, Reason: reason}
	s.sendWatchNotification(watch, methodResyncRequired, params)
}

// Keep a Watch's delivery and stop acknowledgement in one order: unwatch may
// wait for an in-progress bounded write, but no notification can follow its response.
func (s *connectionSession) sendWatchNotification(watch *connectionWatch, method string, params any) bool {
	watch.mu.Lock()
	defer watch.mu.Unlock()
	if watch.stopped {
		return false
	}
	return s.server.Notify(s.ctx, method, params) == nil
}

func notificationFits(method string, params any) bool {
	message := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	encoded, err := json.Marshal(message)
	return err == nil && len(encoded) <= maxFrameBytes
}

func (s *connectionSession) removeWatch(id string, expected *connectionWatch) bool {
	s.mu.Lock()
	watch := s.watches[id]
	if watch == nil || (expected != nil && watch != expected) {
		s.mu.Unlock()
		return false
	}
	delete(s.watches, id)
	s.mu.Unlock()
	stopWatch(watch)
	return true
}

func (s *connectionSession) close() {
	s.cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	watches := make([]*connectionWatch, 0, len(s.watches))
	for id, watch := range s.watches {
		delete(s.watches, id)
		watches = append(watches, watch)
	}
	s.mu.Unlock()
	for _, watch := range watches {
		stopWatch(watch)
	}
	s.workers.Wait()
}

func stopWatch(watch *connectionWatch) {
	watch.mu.Lock()
	watch.stopped = true
	watch.mu.Unlock()
	_ = watch.stream.Close()
}
