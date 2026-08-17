package core

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestSnapshotStreamHonorsCancellationBeforeReadingAvailableSnapshot(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	stream, err := manager.Watch(context.Background())
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
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		manager := newManager(managerOptions{
			host:      HostAlias("development"),
			connector: oneSessionConnector{session: session},
		})
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}()
		synctest.Wait()
		stream, err := manager.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		defer stream.Close()
		if _, err := stream.Next(t.Context()); err != nil {
			t.Fatalf("initial Next: %v", err)
		}
		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{loopbackListener(8080)}}
		session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{loopbackListener(8081)}}
		synctest.Wait()
		latest, err := stream.Next(t.Context())
		if err != nil {
			t.Fatalf("coalesced Next: %v", err)
		}
		if latest.Host == nil || len(latest.Host.ListenerObservations) != 1 || latest.Host.ListenerObservations[0].RemotePort != 8081 {
			t.Fatalf("coalesced Snapshot = %#v, want the latest observation", latest.Host)
		}
	})
}

func TestManagerCloseWakesSnapshotStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager()
		stream, err := manager.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if _, err := stream.Next(t.Context()); err != nil {
			t.Fatalf("initial Next: %v", err)
		}
		nextDone := make(chan error, 1)
		go func() {
			_, err := stream.Next(t.Context())
			nextDone <- err
		}()
		synctest.Wait()
		if err := manager.Close(t.Context()); err != nil {
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
	})
}

func TestWatchRejectsConcurrentNextWithoutClosingStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager()
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}()
		stream, err := manager.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if _, err := stream.Next(t.Context()); err != nil {
			t.Fatalf("initial Next: %v", err)
		}

		firstContext, cancelFirst := context.WithCancel(t.Context())
		firstDone := make(chan error, 1)
		go func() {
			_, err := stream.Next(firstContext)
			firstDone <- err
		}()
		synctest.Wait()
		secondContext, cancelSecond := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancelSecond()
		if _, err := stream.Next(secondContext); !errors.Is(err, ErrConcurrentSnapshotNext) {
			t.Fatalf("concurrent Next error = %v, want ErrConcurrentSnapshotNext", err)
		}
		cancelFirst()
		if err := <-firstDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Next error = %v, want context.Canceled", err)
		}

		thirdContext, cancelThird := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancelThird()
		if _, err := stream.Next(thirdContext); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Next after cancellation error = %v, want context deadline", err)
		}
	})
}

func TestWatchEnforcesManagerLimitAndReleasesCapacity(t *testing.T) {
	manager := NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	streams := make([]SnapshotStream, 128)
	for index := range streams {
		stream, err := manager.Watch(context.Background())
		if err != nil {
			t.Fatalf("Watch %d: %v", index, err)
		}
		streams[index] = stream
	}
	_, err := manager.Watch(context.Background())
	var domainError *DomainError
	if !errors.As(err, &domainError) || domainError.Kind != ErrorWatchLimit || !domainError.Retryable {
		t.Fatalf("Watch over limit error = %#v, want retryable watch_limit", err)
	}
	if err := streams[0].Close(); err != nil {
		t.Fatalf("close Watch: %v", err)
	}
	replacement, err := manager.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch after releasing capacity: %v", err)
	}
	_ = replacement.Close()
	for _, stream := range streams[1:] {
		_ = stream.Close()
	}
}

func TestWatchReturnsSubscriptionSnapshotBeforeCoalescedLatest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		manager := newManager(managerOptions{
			host:      HostAlias("development"),
			connector: oneSessionConnector{session: session},
		})
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}()
		synctest.Wait()
		stream, err := manager.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		defer stream.Close()
		initial, err := stream.Next(t.Context())
		if err != nil {
			t.Fatalf("initial Next: %v", err)
		}
		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{loopbackListener(8080)}}
		synctest.Wait()
		latest, err := stream.Next(t.Context())
		if err != nil {
			t.Fatalf("latest Next: %v", err)
		}
		if latest.Revision <= initial.Revision {
			t.Fatalf("latest revision %d did not advance past initial %d", latest.Revision, initial.Revision)
		}
		if latest.Host == nil || len(latest.Host.ListenerObservations) != 1 {
			t.Fatalf("latest Snapshot = %#v, want one observation", latest.Host)
		}
		latest.Host.ListenerObservations[0].RemotePort = 9
		current, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after caller mutation: %v", err)
		}
		if current.Host.ListenerObservations[0].RemotePort != 8080 {
			t.Fatalf("caller mutation changed canonical Snapshot: %#v", current.Host.ListenerObservations)
		}
	})
}
