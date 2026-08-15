package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSnapshotStreamHonorsCancellationBeforeReadingAvailableSnapshot(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	stream, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next error = %v, want context.Canceled", err)
	}
	if snapshot, err := stream.Next(context.Background()); err != nil || snapshot.Revision != 0 {
		t.Fatalf("Next after cancellation = %#v, %v; want initial revision 0", snapshot, err)
	}
}

func TestWatchCoalescesUnreadSnapshotsToLatestRevision(t *testing.T) {
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
	owner.release()
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: blockingConnector{started: make(chan HostAlias, 1)},
		forwardAllocator: scriptedForwardAllocator{
			requests: make(chan forwardSpec, 1),
			owner:    owner,
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	stream, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("initial Next: %v", err)
	}
	added, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	if _, err := manager.Execute(context.Background(), RemoveForward{
		CommandID: CommandID("operation-remove"),
		ForwardID: added.Forward.ID,
	}); err != nil {
		t.Fatalf("remove Manual Forward: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	latest, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("coalesced Next: %v", err)
	}
	if latest.Revision != 2 || len(latest.Hosts[0].Forwards) != 0 {
		t.Fatalf("coalesced Snapshot = %#v, want revision 2 without Forwards", latest)
	}
}

func TestManagerCloseWakesSnapshotStream(t *testing.T) {
	manager := NewManager()
	stream, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("initial Next: %v", err)
	}
	nextDone := make(chan error, 1)
	go func() {
		_, err := stream.Next(context.Background())
		nextDone <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Manager.Close: %v", err)
	}
	select {
	case err := <-nextDone:
		var domainError *DomainError
		if !errors.As(err, &domainError) || domainError.Kind != ErrorManagerClosed {
			t.Fatalf("Next after Manager.Close error = %v, want manager_closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Manager.Close did not wake Next")
	}
}

func TestWatchRejectsConcurrentNextWithoutClosingStream(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	stream, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("initial Next: %v", err)
	}

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := stream.Next(firstContext)
		firstDone <- err
	}()
	time.Sleep(10 * time.Millisecond)
	secondContext, cancelSecond := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelSecond()
	if _, err := stream.Next(secondContext); !errors.Is(err, ErrConcurrentSnapshotNext) {
		t.Fatalf("concurrent Next error = %v, want ErrConcurrentSnapshotNext", err)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Next error = %v, want context.Canceled", err)
	}

	thirdContext, cancelThird := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelThird()
	if _, err := stream.Next(thirdContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next after cancellation error = %v, want context deadline", err)
	}
}

func TestWatchEnforcesManagerLimitAndReleasesCapacity(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	streams := make([]SnapshotStream, 128)
	for index := range streams {
		stream, err := manager.Watch(context.Background(), WatchOptions{})
		if err != nil {
			t.Fatalf("Watch %d: %v", index, err)
		}
		streams[index] = stream
	}
	_, err := manager.Watch(context.Background(), WatchOptions{})
	var domainError *DomainError
	if !errors.As(err, &domainError) || domainError.Kind != ErrorWatchLimit || !domainError.Retryable {
		t.Fatalf("Watch over limit error = %#v, want retryable watch_limit", err)
	}
	if err := streams[0].Close(); err != nil {
		t.Fatalf("close Watch: %v", err)
	}
	replacement, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch after releasing capacity: %v", err)
	}
	_ = replacement.Close()
	for _, stream := range streams[1:] {
		_ = stream.Close()
	}
}

func TestWatchReturnsSubscriptionSnapshotBeforeCoalescedLatest(t *testing.T) {
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
		owner.release()
		_ = manager.Close(context.Background())
	})

	stream, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if _, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	initial, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("initial Next: %v", err)
	}
	wantInitial := Snapshot{
		Revision: 0,
		Hosts: []HostSnapshot{{
			Alias:                HostAlias("development"),
			Connection:           ConnectionDisconnected,
			Discovery:            stoppedDiscovery(),
			ListenerObservations: []ListenerObservation{},
			Forwards:             []ForwardSnapshot{},
		}},
	}
	if !reflect.DeepEqual(initial, wantInitial) {
		t.Fatalf("initial Snapshot = %#v, want %#v", initial, wantInitial)
	}

	latest, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("latest Next: %v", err)
	}
	wantLatest := Snapshot{
		Revision: 1,
		Hosts: []HostSnapshot{{
			Alias:                HostAlias("development"),
			Connection:           ConnectionConnecting,
			Discovery:            stoppedDiscovery(),
			ListenerObservations: []ListenerObservation{},
			Forwards:             []ForwardSnapshot{owner.projection},
		}},
	}
	if !reflect.DeepEqual(latest, wantLatest) {
		t.Fatalf("latest Snapshot = %#v, want %#v", latest, wantLatest)
	}

	latest.Hosts[0].Forwards[0].LocalFamilies[0] = FamilyIPv6
	current, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot after caller mutation: %v", err)
	}
	if !reflect.DeepEqual(current, wantLatest) {
		t.Fatalf("caller mutation changed canonical Snapshot: %#v", current)
	}
}
