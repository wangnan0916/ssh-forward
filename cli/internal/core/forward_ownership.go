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

var errLocalEndpointConflict = errors.New("Local Endpoint allocation conflict")

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
		if errors.Is(err, proxy.ErrLocalPortConflict) {
			return nil, errLocalEndpointConflict
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

func closeOwnedForward(forward ownedForward) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = forward.Close(ctx)
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
