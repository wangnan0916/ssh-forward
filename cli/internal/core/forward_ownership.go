package core

import (
	"cmp"
	"context"
	"errors"
	"net/netip"
	"slices"
	"time"

	"ssh-forward/cli/internal/proxy"
)

type forwardSpec struct {
	ID                 ForwardID
	Kind               ForwardKind
	Remote             netip.AddrPort
	PreferredLocalPort uint16
}

type ownedForward interface {
	Projection() ForwardSnapshot
	Close(context.Context) error
}

type forwardAllocator interface {
	Allocate(context.Context, forwardSpec) (ownedForward, error)
}

type proxyForwardAllocator struct {
	dialer proxy.Dialer
}

func (a proxyForwardAllocator) Allocate(ctx context.Context, spec forwardSpec) (ownedForward, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := proxy.OpenEndpoint(proxy.EndpointOptions{
		PreferredPort: spec.PreferredLocalPort,
		Remote:        spec.Remote,
		Dialer:        a.dialer,
	})
	if err != nil {
		// The allocator is the seam boundary: the proxy's bare conflict error
		// is translated to the domain error here, exactly once — the wire
		// mapping in the adapter is the only other mention of this failure.
		if errors.Is(err, proxy.ErrLocalPortConflict) {
			return nil, &DomainError{Kind: ErrorLocalPortConflict, Retryable: true}
		}
		return nil, err
	}
	owner := &proxyOwnedForward{spec: spec, endpoint: endpoint}
	if err := ctx.Err(); err != nil {
		closeOwnedForward(owner)
		return nil, err
	}
	return owner, nil
}

type proxyOwnedForward struct {
	spec     forwardSpec
	endpoint *proxy.Endpoint
}

func (f *proxyOwnedForward) Projection() ForwardSnapshot {
	return ForwardSnapshot{
		ID:                 f.spec.ID,
		Kind:               f.spec.Kind,
		RemotePort:         f.spec.Remote.Port(),
		RemoteFamily:       familyForAddress(f.spec.Remote.Addr()),
		AllocatedLocalPort: f.endpoint.LocalPort(),
		LocalFamilies:      []AddressFamily{FamilyIPv4, FamilyIPv6},
	}
}

func (f *proxyOwnedForward) Close(ctx context.Context) error {
	return f.endpoint.Close(ctx)
}

// closeWithTimeout bounds a best-effort Close. Forward endpoints and
// Forwarding Sessions share this one habit — a bounded, fire-and-forget
// teardown — differing only in how long a slow Close may take.
func closeWithTimeout(close func(context.Context) error, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = close(ctx)
}

func closeOwnedForward(forward ownedForward) {
	closeWithTimeout(forward.Close, time.Second)
}

type forwardEntry struct {
	owner        ownedForward
	removalOwner CommandID
}

type forwardTable struct {
	entries map[ForwardID]*forwardEntry
}

func newForwardTable() forwardTable {
	return forwardTable{entries: make(map[ForwardID]*forwardEntry)}
}

func (t *forwardTable) add(owner ownedForward) bool {
	id := owner.Projection().ID
	if _, found := t.entries[id]; found {
		return false
	}
	t.entries[id] = &forwardEntry{owner: owner}
	return true
}

// removeDirect removes one forward outside the command protocol; only the
// reconciliation worker calls it. It returns the owner when this call
// removed the entry, so teardown happens exactly once: a command that
// already reserved the removal has taken the entry out of reach.
func (t *forwardTable) removeDirect(id ForwardID) (ownedForward, bool) {
	entry, found := t.entries[id]
	if !found || entry.removalOwner != "" {
		return nil, false
	}
	delete(t.entries, id)
	return entry.owner, true
}

// hasManagedForListener reports whether a Managed Forward already serves the
// given listener key. The worker uses it to keep at most one Managed
// Forward per Listener; the approve command uses it to record an approval
// without duplicating a forward an auto policy already created.
func (t *forwardTable) hasManagedForListener(key remoteListenerKey, observation ListenerObservation) bool {
	for _, entry := range t.entries {
		projection := entry.owner.Projection()
		if projection.Kind != ForwardManaged {
			continue
		}
		if managedKey, known := managedForwardKey(projection.ID); known && managedKey == key {
			return true
		}
	}
	return false
}

type removalReservationState uint8

const (
	removalMissing removalReservationState = iota
	removalAvailable
	removalInProgress
)

func (t *forwardTable) reserveRemoval(id ForwardID, commandID CommandID) (ownedForward, ForwardSnapshot, CommandID, removalReservationState) {
	entry, found := t.entries[id]
	if !found {
		return nil, ForwardSnapshot{}, "", removalMissing
	}
	projection := entry.owner.Projection()
	if entry.removalOwner != "" {
		return nil, projection, entry.removalOwner, removalInProgress
	}
	entry.removalOwner = commandID
	return entry.owner, projection, commandID, removalAvailable
}

func (t *forwardTable) completeRemoval(id ForwardID, commandID CommandID) (ForwardSnapshot, bool) {
	entry, found := t.entries[id]
	if !found || entry.removalOwner != commandID {
		return ForwardSnapshot{}, false
	}
	projection := entry.owner.Projection()
	delete(t.entries, id)
	return projection, true
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

func (t *forwardTable) owners() []ownedForward {
	owners := make([]ownedForward, 0, len(t.entries))
	for _, entry := range t.entries {
		owners = append(owners, entry.owner)
	}
	return owners
}
