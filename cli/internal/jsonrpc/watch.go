package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/creachadair/jrpc2"

	"ssh-forward/cli/internal/core"
)

type connectionSession struct {
	ctx          context.Context
	cancel       context.CancelFunc
	manager      core.Manager
	capabilities negotiatedCapabilities
	channel      *pendingChannel
	server       *jrpc2.Server

	mu             sync.Mutex
	workers        sync.WaitGroup
	closed         bool
	pendingWatches int
	nextWatchID    uint64
	watches        map[string]*connectionWatch
}

type connectionWatch struct {
	id       string
	stream   core.SnapshotStream
	activate chan struct{}

	mu      sync.Mutex
	stopped bool
}

func newConnectionSession(ctx context.Context, manager core.Manager, capabilities negotiatedCapabilities, channel *pendingChannel) *connectionSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	return &connectionSession{
		ctx:          sessionCtx,
		cancel:       cancel,
		manager:      manager,
		capabilities: capabilities,
		channel:      channel,
		watches:      make(map[string]*connectionWatch),
	}
}

func (s *connectionSession) handleWatch(ctx context.Context, request *jrpc2.Request) (any, error) {
	if !s.capabilities.watchSnapshot {
		return nil, errWatchCapabilityRequired
	}
	var params snapshotParams
	if paramsText := request.ParamString(); paramsText == "" || json.Unmarshal([]byte(paramsText), &params) != nil || params.Scope.Kind != "all" {
		return nil, errInvalidScope
	}
	if !s.reserveWatchSlot() {
		return nil, errWatchLimit
	}
	reserved := true
	defer func() {
		if reserved {
			s.releaseWatchSlot()
		}
	}()

	stream, err := s.manager.Watch(ctx, core.WatchOptions{})
	if err != nil {
		return nil, marshalManagerError(err)
	}
	initial, err := stream.Next(ctx)
	if err != nil {
		_ = stream.Close()
		return nil, &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = stream.Close()
		return nil, &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
	}
	s.pendingWatches--
	reserved = false
	s.nextWatchID++
	watchID := "watch-" + strconv.FormatUint(s.nextWatchID, 10)
	watch := &connectionWatch{
		id:       watchID,
		stream:   stream,
		activate: make(chan struct{}),
	}
	s.watches[watchID] = watch
	s.mu.Unlock()

	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.runWatch(watch)
	}()
	return watchResult{
		WatchID:  watchID,
		Snapshot: marshalSnapshot(initial),
	}, nil
}

// watchResponseID reports the server-assigned Watch ID introduced by a
// manager.watch response that carries a Snapshot.
func watchResponseID(message []byte) (string, bool) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(message, &envelope) != nil || len(envelope.Result) == 0 {
		return "", false
	}
	var result struct {
		WatchID  string          `json:"watch_id"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(envelope.Result, &result) != nil || result.WatchID == "" || len(result.Snapshot) == 0 {
		return "", false
	}
	return result.WatchID, true
}

// onResponseSent runs after a response frame is written, so no notification
// can overtake the response that introduces a Watch. The handler committed
// the Watch to s.watches before the response was sent, so the lookup always
// succeeds for watch responses; the guard keeps the activation channel closed
// at most once.
func (s *connectionSession) onResponseSent(message []byte) {
	watchID, ok := watchResponseID(message)
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

func (s *connectionSession) runWatch(watch *connectionWatch) {
	defer s.removeWatch(watch.id, watch)
	select {
	case <-watch.activate:
	case <-s.ctx.Done():
		return
	}
	for {
		snapshot, err := watch.stream.Next(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil && errors.Is(err, core.ErrResyncRequired) {
				s.sendResyncRequired(watch, "manager_resync_required")
			}
			return
		}
		params := snapshotNotification{WatchID: watch.id, Snapshot: marshalSnapshot(snapshot)}
		if !notificationFits("manager.snapshot", params) {
			s.sendResyncRequired(watch, "snapshot_too_large")
			return
		}
		if !s.sendWatchNotification(watch, "manager.snapshot", params) {
			return
		}
	}
}

func (s *connectionSession) sendResyncRequired(watch *connectionWatch, reason string) {
	params := resyncNotification{WatchID: watch.id, Reason: reason}
	if notificationFits("manager.resync_required", params) {
		s.sendWatchNotification(watch, "manager.resync_required", params)
	}
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
