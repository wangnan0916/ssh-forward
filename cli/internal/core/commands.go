package core

import "context"

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

// completeCommand is the single completion point for an admitted command: it
// records the outcome and releases the command's worker slot. Every handler
// reaches exactly one of completeCommand (success or async-completion path)
// or failCommandAndRelease (error path); the worker release always rides
// along, never sprinkled at call sites.
func (m *manager) completeCommand(id CommandID, command Command, outcome Outcome) {
	m.mu.Lock()
	m.completeCommandLocked(id, command, outcome)
	m.mu.Unlock()
	m.workers.Done()
}

// failCommandAndRelease rejects an admitted command and releases its worker
// slot; it mirrors completeCommand for the error paths.
func (m *manager) failCommandAndRelease(id CommandID) {
	m.failCommand(id)
	m.workers.Done()
}

// maxCommandRecords bounds the command journal: each completed command is
// retained so a replayed operation ID answers from memory, but the journal
// is FIFO-bounded so a client cycling fresh operation IDs cannot grow the
// Manager's memory without limit. Persistence replay (slice 5) will replace
// this in-memory window with the durable journal.
const maxCommandRecords = 1024

func (m *manager) completeCommandLocked(id CommandID, command Command, outcome Outcome) {
	if _, found := m.commands[id]; !found {
		m.commandOrder = append(m.commandOrder, id)
	}
	m.commands[id] = commandRecord{command: command, outcome: cloneOutcome(outcome)}
	if len(m.commandOrder) > maxCommandRecords {
		oldest := m.commandOrder[0]
		m.commandOrder = m.commandOrder[1:]
		delete(m.commands, oldest)
	}
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
