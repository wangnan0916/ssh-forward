package core

import (
	"cmp"
	"context"
	"errors"
	"net/netip"
	"slices"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
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
	owner ownedForward
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

// removeDirect removes one forward; only the reconciliation worker calls it.
func (t *forwardTable) removeDirect(id ForwardID) (ownedForward, bool) {
	entry, found := t.entries[id]
	if !found {
		return nil, false
	}
	delete(t.entries, id)
	return entry.owner, true
}

// managedForwardEntry pairs a Managed Forward's identity with the listener
// key it serves, in one pass over the table: reconciliation needs exactly
// this shape for the has-managed set and the removal candidates.
type managedForwardEntry struct {
	id  ForwardID
	key remoteListenerKey
}

// managedForwardsLocked lists the Managed Forward entries in one pass, with
// no cloning or sorting: the reconciliation worker iterates this instead of
// the full table snapshot (Manual Forwards are irrelevant to its delta).
func (t *forwardTable) managedForwardsLocked() []managedForwardEntry {
	entries := make([]managedForwardEntry, 0, len(t.entries))
	for _, entry := range t.entries {
		projection := entry.owner.Projection()
		if projection.Kind != ForwardManaged {
			continue
		}
		if managedKey, known := managedForwardKey(projection.ID); known {
			entries = append(entries, managedForwardEntry{id: projection.ID, key: managedKey})
		}
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

func (t *forwardTable) owners() []ownedForward {
	owners := make([]ownedForward, 0, len(t.entries))
	for _, entry := range t.entries {
		owners = append(owners, entry.owner)
	}
	return owners
}
