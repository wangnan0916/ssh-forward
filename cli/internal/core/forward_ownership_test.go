package core

import (
	"context"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"
)

type scriptedForwardAllocator struct {
	requests chan forwardSpec
	owner    ownedForward
}

func (a scriptedForwardAllocator) Allocate(_ context.Context, spec forwardSpec) (ownedForward, error) {
	a.requests <- spec
	return a.owner, nil
}

type scriptedOwnedForward struct {
	projection  ForwardSnapshot
	closeStart  chan struct{}
	closeDone   chan struct{}
	closeOnce   sync.Once
	releaseOnce sync.Once
}

func (f *scriptedOwnedForward) Projection() ForwardSnapshot {
	return cloneForward(f.projection)
}

func (f *scriptedOwnedForward) Close(ctx context.Context) error {
	f.closeOnce.Do(func() { close(f.closeStart) })
	select {
	case <-f.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *scriptedOwnedForward) release() {
	f.releaseOnce.Do(func() { close(f.closeDone) })
}

func TestManagerOwnsForwardAndLocalEndpointAsOneLifetime(t *testing.T) {
	owner := &scriptedOwnedForward{
		projection: ForwardSnapshot{
			ID:                 ForwardID("manual:operation-add"),
			Kind:               ForwardManual,
			RemotePort:         8080,
			RemoteFamily:       FamilyIPv4,
			AllocatedLocalPort: 8087,
			LocalFamilies:      []AddressFamily{FamilyIPv4},
		},
		closeStart: make(chan struct{}),
		closeDone:  make(chan struct{}),
	}
	allocator := scriptedForwardAllocator{
		requests: make(chan forwardSpec, 1),
		owner:    owner,
	}
	manager := newManager(managerOptions{
		host:             HostAlias("development"),
		connector:        blockingConnector{started: make(chan HostAlias, 1)},
		forwardAllocator: allocator,
	})
	t.Cleanup(func() {
		owner.release()
		_ = manager.Close(context.Background())
	})

	added, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyIPv4,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	if !reflect.DeepEqual(added.Forward, owner.projection) {
		t.Fatalf("added Forward = %#v, want owner projection %#v", added.Forward, owner.projection)
	}
	request := <-allocator.requests
	wantRequest := forwardSpec{
		ID:                 ForwardID("manual:operation-add"),
		Kind:               ForwardManual,
		Remote:             netip.MustParseAddrPort("127.0.0.1:8080"),
		PreferredLocalPort: 8080,
	}
	if request != wantRequest {
		t.Fatalf("allocation request = %#v, want %#v", request, wantRequest)
	}

	removed := make(chan removeResult, 1)
	go func() {
		outcome, err := manager.Execute(context.Background(), RemoveForward{
			CommandID: CommandID("operation-remove"),
			ForwardID: owner.projection.ID,
		})
		removed <- removeResult{outcome: outcome, err: err}
	}()
	select {
	case <-owner.closeStart:
	case <-time.After(time.Second):
		t.Fatal("removal did not begin Local Endpoint closure")
	}
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot during removal: %v", err)
	}
	if len(snapshot.Host.Forwards) != 1 {
		t.Fatalf("Forward disappeared before Local Endpoint stopped: %#v", snapshot)
	}
	owner.release()
	result := <-removed
	if result.err != nil {
		t.Fatalf("remove Manual Forward: %v", result.err)
	}
	if !reflect.DeepEqual(result.outcome.Forward, owner.projection) {
		t.Fatalf("removed Forward = %#v, want owner projection %#v", result.outcome.Forward, owner.projection)
	}
	snapshot, err = manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot after removal: %v", err)
	}
	if len(snapshot.Host.Forwards) != 0 {
		t.Fatalf("removed Forward remains visible: %#v", snapshot)
	}
}
