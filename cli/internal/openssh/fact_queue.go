package openssh

import (
	"context"
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
//
// Proportionality: a real scanner session emits at most two facts per
// connection (one ObservationSet, one terminal change), so with a cap of 8
// the overflow branches below are unreachable today. They exist to keep the
// Baseline guarantee if a future producer outpaces the consumer; they are
// defensive depth, deliberately untested because the constitution tests only
// reachable behavior.
//
// The consumer surface is next: one call that blocks until a fact is
// available or the queue is drained and closed, so the blocking protocol
// (wakeup channel, closed interpretation) lives here, not in the Session.
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

// next blocks until a fact is available, the queue is drained and closed, or
// ctx is done. The Session's done channel participates so a closing Session
// unblocks the wait; the scanner always closes the queue before done, so the
// drained path waits only for done (or ctx) and the Session then translates
// its terminal error without blocking again. drained=true means the queue
// has no more facts and is closed; trailing facts are always returned first.
func (q *sessionFactQueue) next(ctx context.Context, sessionDone <-chan struct{}) (core.SessionFact, bool, error) {
	for {
		q.mu.Lock()
		fact, found, drained := q.popLocked()
		q.mu.Unlock()
		if found {
			return fact, false, nil
		}
		if drained {
			select {
			case <-sessionDone:
				return nil, true, nil
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
		select {
		case <-q.notify:
		case <-sessionDone:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// popLocked removes the oldest pending fact, or the held terminal
// DiscoveryChange once everything else has drained.
func (q *sessionFactQueue) popLocked() (core.SessionFact, bool, bool) {
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
