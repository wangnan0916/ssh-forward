package core

import (
	"fmt"
	"testing"
)

// TestCommandJournalIsBounded pins the FIFO retention of the command journal:
// a client cycling fresh operation IDs must not grow the Manager's memory
// without limit. The oldest records are evicted first; a replayed operation
// ID older than the window re-executes instead of answering from memory.
func TestCommandJournalIsBounded(t *testing.T) {
	m := &manager{commands: make(map[CommandID]commandRecord)}
	for i := 0; i < maxCommandRecords+5; i++ {
		id := CommandID(fmt.Sprintf("op-%d", i))
		m.completeCommandLocked(id, nil, Outcome{Kind: OutcomeForwardAdded})
	}
	if len(m.commands) != maxCommandRecords {
		t.Fatalf("journal holds %d records, want %d", len(m.commands), maxCommandRecords)
	}
	for i := 0; i < 5; i++ {
		id := CommandID(fmt.Sprintf("op-%d", i))
		if _, found := m.commands[id]; found {
			t.Fatalf("oldest command %q survived eviction", id)
		}
	}
	latest := CommandID(fmt.Sprintf("op-%d", maxCommandRecords+4))
	if _, found := m.commands[latest]; !found {
		t.Fatalf("newest command %q was evicted", latest)
	}
}
