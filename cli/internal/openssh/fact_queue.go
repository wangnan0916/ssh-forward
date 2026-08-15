package openssh

import (
	"sync"

	"ssh-forward/cli/internal/core"
)

const maxQueuedSessionFacts = 8

type sessionFactQueue struct {
	mu                sync.Mutex
	items             []core.SessionFact
	notify            chan struct{}
	closed            bool
	baselineDelivered bool
	terminalDiscovery *core.DiscoveryChange
}

func newSessionFactQueue() *sessionFactQueue {
	return &sessionFactQueue{
		items:  make([]core.SessionFact, 0, maxQueuedSessionFacts),
		notify: make(chan struct{}, 1),
	}
}

func (q *sessionFactQueue) push(fact core.SessionFact) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if change, ok := fact.(core.DiscoveryChange); ok && change.State == core.DiscoveryFailed {
		q.terminalDiscovery = &change
		q.signalLocked()
		return
	}
	if len(q.items) < maxQueuedSessionFacts {
		q.items = append(q.items, fact)
		q.signalLocked()
		return
	}
	last := len(q.items) - 1
	if !q.baselineDelivered {
		if _, protected := q.items[last].(core.ObservationSet); protected {
			q.signalLocked()
			return
		}
	}
	q.items[last] = fact
	q.signalLocked()
}

func (q *sessionFactQueue) pop() (core.SessionFact, bool, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		if q.terminalDiscovery == nil {
			return nil, false, q.closed
		}
		fact := *q.terminalDiscovery
		q.terminalDiscovery = nil
		return fact, true, q.closed
	}
	fact := q.items[0]
	copy(q.items, q.items[1:])
	q.items = q.items[:len(q.items)-1]
	if _, ok := fact.(core.ObservationSet); ok {
		q.baselineDelivered = true
	}
	if len(q.items) != 0 {
		q.signalLocked()
	}
	return fact, true, q.closed
}

func (q *sessionFactQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.signalLocked()
}

func (q *sessionFactQueue) signalLocked() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
