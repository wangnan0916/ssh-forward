package core

import (
	"context"
	"time"

	"ssh-forward/cli/internal/proxy"
)

type commandRecord struct {
	command Command
	outcome Outcome
}

type pendingCommand struct {
	command Command
	done    chan struct{}
}

func (m *manager) beginCommand(ctx context.Context, id CommandID, command Command) (Outcome, bool, error) {
	if id == "" {
		return Outcome{}, false, &DomainError{Kind: ErrorInvalidCommand}
	}
	for {
		if err := ctx.Err(); err != nil {
			return Outcome{}, false, err
		}
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return Outcome{}, false, err
		}
		if m.closed {
			m.mu.Unlock()
			return Outcome{}, false, &DomainError{Kind: ErrorManagerClosed, Retryable: true}
		}
		if record, found := m.commands[id]; found {
			m.mu.Unlock()
			if !sameCommand(record.command, command) {
				return Outcome{}, true, &DomainError{Kind: ErrorCommandIDConflict}
			}
			return cloneOutcome(record.outcome), true, nil
		}
		if pending, found := m.pending[id]; found {
			if !sameCommand(pending.command, command) {
				m.mu.Unlock()
				return Outcome{}, true, &DomainError{Kind: ErrorCommandIDConflict}
			}
			done := pending.done
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return Outcome{}, false, ctx.Err()
			case <-done:
				continue
			}
		}
		m.pending[id] = &pendingCommand{command: command, done: make(chan struct{})}
		m.workers.Add(1)
		m.mu.Unlock()
		return Outcome{}, false, nil
	}
}

func (m *manager) failCommand(id CommandID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failCommandLocked(id)
}

func (m *manager) failCommandLocked(id CommandID) {
	pending := m.pending[id]
	delete(m.pending, id)
	if pending != nil {
		close(pending.done)
	}
}

func (m *manager) completeCommandLocked(id CommandID, command Command, outcome Outcome) {
	m.commands[id] = commandRecord{command: command, outcome: cloneOutcome(outcome)}
	m.failCommandLocked(id)
}

func sameCommand(left, right Command) bool {
	switch left := left.(type) {
	case AddManualForward:
		right, ok := right.(AddManualForward)
		return ok && left == right
	case RemoveForward:
		right, ok := right.(RemoveForward)
		return ok && left == right
	default:
		return false
	}
}

func closeEndpoint(endpoint *proxy.Endpoint) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = endpoint.Close(ctx)
}
