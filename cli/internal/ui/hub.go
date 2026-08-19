package ui

import (
	"context"
	"sync"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// watchHub is one Manager Watch for the UI process. SSE clients subscribe
// locally; they do not each open a Watch.
type watchHub struct {
	manager core.Manager

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	latest  core.Snapshot
	failed  error
	subs    map[chan core.Snapshot]struct{}
}

func newWatchHub(manager core.Manager) *watchHub {
	return &watchHub{manager: manager, subs: make(map[chan core.Snapshot]struct{})}
}

func (h *watchHub) subscribe() (core.Snapshot, <-chan core.Snapshot, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed != nil {
		return core.Snapshot{}, nil, func() {}, h.failed
	}
	ch := make(chan core.Snapshot, 1)
	h.subs[ch] = struct{}{}
	initial := h.latest
	if !h.started {
		h.started = true
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.loop(ctx)
	}
	return initial, ch, func() { h.unsubscribe(ch) }, nil
}

func (h *watchHub) unsubscribe(ch chan core.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, ch)
}

func (h *watchHub) stop() {
	h.mu.Lock()
	cancel := h.cancel
	h.cancel = nil
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (h *watchHub) loop(ctx context.Context) {
	stream, err := h.manager.Watch(ctx)
	if err != nil {
		h.fail(err)
		return
	}
	defer stream.Close()
	for {
		snap, err := stream.Next(ctx)
		if err != nil {
			h.fail(err)
			return
		}
		h.publish(snap)
	}
}

func (h *watchHub) publish(snap core.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed != nil {
		return
	}
	h.latest = snap
	for ch := range h.subs {
		select {
		case ch <- snap:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snap:
			default:
			}
		}
	}
}

func (h *watchHub) fail(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed != nil {
		return
	}
	h.failed = err
	for ch := range h.subs {
		close(ch)
	}
	h.subs = nil
}
