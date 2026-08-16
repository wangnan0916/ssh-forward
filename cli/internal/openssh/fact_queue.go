package openssh

import (
	"sync"

	"ssh-forward/cli/internal/core"
)

const maxQueuedSessionFacts = 8

// sessionFactQueue is a bounded fact buffer between the scanner and the
// Forwarding Session consumer. On overflow it replaces the newest pending
// fact rather than dropping the oldest, so the head — where the first
// ObservationSet sits — survives. Transport guarantee: that first
// ObservationSet is never evicted, because core's Discovery Baseline is the
// first complete observation after connecting and dropping it would silently
// delay baseline establishment past a queue overflow. Every later set may be
// replaced by newer evidence.
type sessionFactQueue struct {
	mu                sync.Mutex
	items             []core.SessionFact
	notify            chan struct{}
	closed            bool
	firstSetDelivered bool
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
	if !q.firstSetDelivered {
		if _, protected := q.items[last].(core.ObservationSet); protected {
			// Keep the first ObservationSet (the future Discovery Baseline)
			// even if it would be evicted: overflow may drop it, but the
			// next scan's set must still establish the Baseline.
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
		// From here on, overflow may replace the newest ObservationSet
		// freely; the transport guarantee applies to the first one only.
		q.firstSetDelivered = true
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
