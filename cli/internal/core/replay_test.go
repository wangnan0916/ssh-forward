package core

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// The operation-ID retry contract (ipc-protocol.md): a replayed command
// with the same operation ID answers from the journal instead of
// re-executing or failing. The slice-5 commands (ApproveListener,
// SuppressListener) participate like the round-1/2 commands.

func TestApproveReplayAnswersFromJournal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, nil)
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8000)) // baseline generation: pre-baseline
		h.waitFor("baseline settles", func(s Snapshot) bool {
			return s.Host != nil && s.Host.Discovery.BaselineEstablished
		})
		h.push(loopbackListener(8080))
		h.waitFor("Ask appears", func(s Snapshot) bool { return askPorts(s)[8080] })

		approve := ApproveListener{CommandID: "op-1", Host: "development", RemotePort: 8080}
		first, err := h.manager.Execute(context.Background(), approve)
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		h.waitFor("Managed Forward appears", func(s Snapshot) bool {
			return len(managedForwards(s)) == 1
		})

		replayed, err := h.manager.Execute(context.Background(), approve)
		if err != nil {
			t.Fatalf("replayed approve: %v", err)
		}
		if replayed.Kind != OutcomeApprovalRecorded {
			t.Fatalf("replay = %+v, want approval_recorded from the journal", replayed)
		}
		if replayed.Forward.ID != first.Forward.ID {
			t.Fatalf("replay forward %q != first forward %q", replayed.Forward.ID, first.Forward.ID)
		}
		if replayed.Revision != first.Revision {
			t.Fatalf("replay revision %d != first revision %d", replayed.Revision, first.Revision)
		}
	})
}

func TestSuppressReplayAnswersFromJournal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, nil)
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8000)) // baseline generation: pre-baseline
		h.waitFor("baseline settles", func(s Snapshot) bool {
			return s.Host != nil && s.Host.Discovery.BaselineEstablished
		})
		h.push(loopbackListener(8080))
		h.waitFor("Ask appears", func(s Snapshot) bool { return askPorts(s)[8080] })

		suppress := SuppressListener{CommandID: "op-2", Host: "development", RemotePort: 8080}
		first, err := h.manager.Execute(context.Background(), suppress)
		if err != nil {
			t.Fatalf("suppress: %v", err)
		}
		if first.Kind != OutcomeSuppressionRecorded {
			t.Fatalf("suppress = %+v, want suppression_recorded", first)
		}
		h.waitFor("Ask drops", func(s Snapshot) bool { return !askPorts(s)[8080] })

		replayed, err := h.manager.Execute(context.Background(), suppress)
		if err != nil {
			t.Fatalf("replayed suppress: %v", err)
		}
		if replayed.Kind != OutcomeSuppressionRecorded || replayed.Revision != first.Revision {
			t.Fatalf("replay = %+v, want the recorded outcome %+v", replayed, first)
		}
	})
}

func TestReplayedOperationIDWithDifferentCommandConflicts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newReconcileHarness(t, nil)
		defer closeReconciliation(t, h)
		h.push(loopbackListener(8000)) // baseline generation: pre-baseline
		h.waitFor("baseline settles", func(s Snapshot) bool {
			return s.Host != nil && s.Host.Discovery.BaselineEstablished
		})
		h.push(loopbackListener(8080))
		h.waitFor("Ask appears", func(s Snapshot) bool { return askPorts(s)[8080] })

		if _, err := h.manager.Execute(context.Background(), ApproveListener{
			CommandID: "op-3", Host: "development", RemotePort: 8080,
		}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		_, err := h.manager.Execute(context.Background(), SuppressListener{
			CommandID: "op-3", Host: "development", RemotePort: 8080,
		})
		var domainError *DomainError
		if err == nil || !errors.As(err, &domainError) || domainError.Kind != ErrorCommandIDConflict {
			t.Fatalf("cross-command replay err = %v, want ErrorCommandIDConflict", err)
		}
	})
}
