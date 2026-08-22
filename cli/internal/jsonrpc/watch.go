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
	ctx     context.Context
	cancel  context.CancelFunc
	manager core.Manager
	server  *jrpc2.Server

	mu          sync.Mutex
	workers     sync.WaitGroup
	closed      bool
	nextWatchID uint64
	watches     map[string]*connectionWatch
}

type connectionWatch struct {
	id     string
	stream core.SnapshotStream

	mu      sync.Mutex
	stopped bool
}

func newConnectionSession(ctx context.Context, manager core.Manager) *connectionSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	return &connectionSession{
		ctx:     sessionCtx,
		cancel:  cancel,
		manager: manager,
		watches: make(map[string]*connectionWatch),
	}
}

func (s *connectionSession) handleWatch(ctx context.Context, request *jrpc2.Request) (any, error) {
	if request.IsNotification() {
		return nil, errInvalidParameters
	}
	stream, err := s.manager.Watch(ctx)
	if err != nil {
		return nil, marshalManagerError(err)
	}
	initial, err := stream.Next(ctx)
	if err != nil {
		_ = stream.Close()
		return nil, internalError()
	}
	watch, ok := s.registerWatch(stream)
	if !ok {
		_ = stream.Close()
		return nil, internalError()
	}

	go func() {
		defer s.workers.Done()
		s.runWatch(watch)
	}()
	return watchResult{
		WatchID:  watch.id,
		Snapshot: snapshot.Encode(initial),
	}, nil
}

func (s *connectionSession) handleUnwatch(_ context.Context, request *jrpc2.Request) (any, error) {
	var params unwatchParams
	if request.UnmarshalParams(&params) != nil || params.WatchID == "" {
		return nil, errInvalidParameters
	}
	stopped := s.removeWatch(params.WatchID, nil)
	return unwatchResult{WatchID: params.WatchID, Stopped: stopped}, nil
}

func (s *connectionSession) registerWatch(stream core.SnapshotStream) (*connectionWatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	s.nextWatchID++
	watch := &connectionWatch{
		id:     "watch-" + strconv.FormatUint(s.nextWatchID, 10),
		stream: stream,
	}
	s.watches[watch.id] = watch
	s.workers.Add(1)
	return watch, true
}

func (s *connectionSession) runWatch(watch *connectionWatch) {
	defer s.removeWatch(watch.id, watch)
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

func (s *connectionSession) sendResyncRequired(watch *connectionWatch, reason string) {
	params := resyncNotification{WatchID: watch.id, Reason: reason}
	s.sendWatchNotification(watch, methodResyncRequired, params)
}

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
