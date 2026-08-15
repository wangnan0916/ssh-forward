package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type removeResult struct {
	outcome Outcome
	err     error
}

func TestIdempotentRemoveWaitsUntilEndpointStops(t *testing.T) {
	manager, owner, added := setupDelayedRemoval(t)
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
	if !reflect.DeepEqual(first.outcome, retry.outcome) {
		t.Fatalf("remove Outcomes differ: %#v and %#v", first.outcome, retry.outcome)
	}
}

func TestCancelledRemoveFinishesCommittedShutdownInBackground(t *testing.T) {
	manager, owner, added := setupDelayedRemoval(t)
	ctx, cancel := context.WithCancel(context.Background())
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
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Hosts[0].Forwards) != 1 {
		t.Fatalf("removal published before Local Endpoint workers stopped: %#v", snapshot)
	}

	releaseForwardClosure(owner)
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err = manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Hosts[0].Forwards) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background removal did not publish")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := manager.Execute(context.Background(), command); err != nil {
		t.Fatalf("idempotent retry after background removal: %v", err)
	}
}

func TestConcurrentDifferentRemoveWaitsForConsistentSnapshot(t *testing.T) {
	manager, owner, added := setupDelayedRemoval(t)
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
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Hosts[0].Forwards) != 0 {
		t.Fatalf("Snapshot after forward_not_found still contains Forward: %#v", snapshot)
	}
}

func setupDelayedRemoval(t *testing.T) (*manager, *scriptedOwnedForward, Outcome) {
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
	t.Cleanup(func() {
		releaseForwardClosure(owner)
		_ = manager.Close(context.Background())
	})
	added, err := manager.Execute(context.Background(), AddManualForward{
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
