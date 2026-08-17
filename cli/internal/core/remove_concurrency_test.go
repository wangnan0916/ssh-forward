package core

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
)

type removeResult struct {
	outcome Outcome
	err     error
}

func TestIdempotentRemoveWaitsUntilEndpointStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, owner, added := newBubbleDelayedRemoval(t)
		defer closeBubbleDelayedRemoval(t, manager, owner)
		command := RemoveForward{CommandID: CommandID("operation-remove"), ForwardID: added.Forward.ID}
		firstDone := executeRemove(manager, command)
		waitForForwardClosure(t, owner)
		retryDone := executeRemove(manager, command)
		assertRemoveStillWaiting(t, retryDone)

		releaseForwardClosure(owner)
		first := <-firstDone
		retry := <-retryDone
		if first.err != nil || retry.err != nil {
			t.Fatalf("remove errors = %v and %v", first.err, retry.err)
		}
		if diff := cmp.Diff(first.outcome, retry.outcome); diff != "" {
			t.Fatalf("remove Outcomes differ (-first +retry):\n%s", diff)
		}
	})
}

func TestCancelledRemoveFinishesCommittedShutdownInBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, owner, added := newBubbleDelayedRemoval(t)
		defer closeBubbleDelayedRemoval(t, manager, owner)
		ctx, cancel := context.WithCancel(t.Context())
		command := RemoveForward{CommandID: CommandID("operation-remove"), ForwardID: added.Forward.ID}
		done := make(chan removeResult, 1)
		go func() {
			outcome, err := manager.Execute(ctx, command)
			done <- removeResult{outcome: outcome, err: err}
		}()
		waitForForwardClosure(t, owner)
		cancel()
		result := <-done
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancelled remove error = %v, want context.Canceled", result.err)
		}
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Host.Forwards) != 1 {
			t.Fatalf("removal published before Local Endpoint workers stopped: %#v", snapshot)
		}

		releaseForwardClosure(owner)
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Host.Forwards) != 0 {
			t.Fatalf("background removal did not publish: %#v", snapshot)
		}
		if _, err := manager.Execute(t.Context(), command); err != nil {
			t.Fatalf("idempotent retry after background removal: %v", err)
		}
	})
}

func TestConcurrentDifferentRemoveWaitsForConsistentSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, owner, added := newBubbleDelayedRemoval(t)
		defer closeBubbleDelayedRemoval(t, manager, owner)
		firstDone := executeRemove(manager, RemoveForward{
			CommandID: CommandID("operation-remove-1"),
			ForwardID: added.Forward.ID,
		})
		waitForForwardClosure(t, owner)
		secondDone := executeRemove(manager, RemoveForward{
			CommandID: CommandID("operation-remove-2"),
			ForwardID: added.Forward.ID,
		})
		assertRemoveStillWaiting(t, secondDone)

		releaseForwardClosure(owner)
		if result := <-firstDone; result.err != nil {
			t.Fatalf("first remove: %v", result.err)
		}
		second := <-secondDone
		var domainError *DomainError
		if !errors.As(second.err, &domainError) || domainError.Kind != ErrorForwardNotFound {
			t.Fatalf("second remove error = %v, want forward_not_found", second.err)
		}
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Host.Forwards) != 0 {
			t.Fatalf("Snapshot after forward_not_found still contains Forward: %#v", snapshot)
		}
	})
}

// newBubbleDelayedRemoval builds a scripted Manager with one added Manual
// Forward whose Local Endpoint closure blocks on the owner's closeStart
// channel, inside a synctest bubble.
func newBubbleDelayedRemoval(t *testing.T) (*manager, *scriptedOwnedForward, Outcome) {
	t.Helper()
	owner := &scriptedOwnedForward{
		projection: ForwardSnapshot{
			ID:                 ForwardID("manual:operation-add"),
			Kind:               ForwardManual,
			RemotePort:         8080,
			RemoteFamily:       FamilyIPv4,
			AllocatedLocalPort: 8087,
			LocalFamilies:      []AddressFamily{FamilyIPv4, FamilyIPv6},
		},
		closeStart: make(chan struct{}),
		closeDone:  make(chan struct{}),
	}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: blockingConnector{started: make(chan HostAlias, 1)},
		forwardAllocator: scriptedForwardAllocator{
			requests: make(chan forwardSpec, 1),
			owner:    owner,
		},
	})
	added, err := manager.Execute(t.Context(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	return manager, owner, added
}

// closeBubbleDelayedRemoval releases the blocked Local Endpoint closure and
// shuts the Manager down inside the bubble.
func closeBubbleDelayedRemoval(t *testing.T, manager *manager, owner *scriptedOwnedForward) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	releaseForwardClosure(owner)
	_ = manager.Close(ctx)
}

func executeRemove(manager Manager, command RemoveForward) <-chan removeResult {
	done := make(chan removeResult, 1)
	go func() {
		outcome, err := manager.Execute(context.Background(), command)
		done <- removeResult{outcome: outcome, err: err}
	}()
	return done
}

func waitForForwardClosure(t *testing.T, owner *scriptedOwnedForward) {
	t.Helper()
	select {
	case <-owner.closeStart:
	case <-time.After(time.Second):
		t.Fatal("Local Endpoint closure did not start")
	}
}

func releaseForwardClosure(owner *scriptedOwnedForward) {
	owner.release()
}

func assertRemoveStillWaiting(t *testing.T, done <-chan removeResult) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("remove returned before Local Endpoint stopped: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
}
