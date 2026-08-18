package core

import (
	"cmp"
	"context"
	"slices"
	"time"

	"net/netip"
)

// ForwardSpec is one Local Endpoint allocation request: the Managed Forward
// identity, the remote loopback target, and the Preferred Local Port.
type ForwardSpec struct {
	ID                 ForwardID
	Remote             netip.AddrPort
	PreferredLocalPort uint16
	key                remoteListenerKey
}

// OwnedForward is one allocated Local Endpoint. The Forward table owns it
// until reconciliation or Close tears it down.
type OwnedForward interface {
	Projection() ForwardSnapshot
	Close(context.Context) error
}

// ForwardAllocator allocates a Local Endpoint for one ForwardSpec.
// Production (proxy.NewAllocator) and tests (in-memory) are the two adapters.
type ForwardAllocator interface {
	Allocate(context.Context, ForwardSpec) (OwnedForward, error)
}

func closeWithTimeout(close func(context.Context) error, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = close(ctx)
}

func closeOwnedForward(forward OwnedForward) {
	closeWithTimeout(forward.Close, time.Second)
}

type forwardEntry struct {
	owner OwnedForward
	key   remoteListenerKey
}

type forwardTable struct {
	entries map[ForwardID]*forwardEntry
}

func newForwardTable() forwardTable {
	return forwardTable{entries: make(map[ForwardID]*forwardEntry)}
}

func (t *forwardTable) add(owner OwnedForward, key remoteListenerKey) bool {
	id := owner.Projection().ID
	if _, found := t.entries[id]; found {
		return false
	}
	t.entries[id] = &forwardEntry{owner: owner, key: key}
	return true
}

func (t *forwardTable) removeDirect(id ForwardID) (OwnedForward, bool) {
	entry, found := t.entries[id]
	if !found {
		return nil, false
	}
	delete(t.entries, id)
	return entry.owner, true
}

type managedForwardEntry struct {
	id  ForwardID
	key remoteListenerKey
}

func (t *forwardTable) managedForwardsLocked() []managedForwardEntry {
	entries := make([]managedForwardEntry, 0, len(t.entries))
	for id, entry := range t.entries {
		entries = append(entries, managedForwardEntry{id: id, key: entry.key})
	}
	return entries
}

func (t *forwardTable) snapshots() []ForwardSnapshot {
	forwards := make([]ForwardSnapshot, 0, len(t.entries))
	for _, entry := range t.entries {
		forwards = append(forwards, cloneForward(entry.owner.Projection()))
	}
	slices.SortFunc(forwards, func(left, right ForwardSnapshot) int {
		return cmp.Compare(left.ID, right.ID)
	})
	return forwards
}

func (t *forwardTable) owners() []OwnedForward {
	owners := make([]OwnedForward, 0, len(t.entries))
	for _, entry := range t.entries {
		owners = append(owners, entry.owner)
	}
	return owners
}

type refusingAllocator struct{}

func (refusingAllocator) Allocate(context.Context, ForwardSpec) (OwnedForward, error) {
	return nil, errTransportUnavailable
}
