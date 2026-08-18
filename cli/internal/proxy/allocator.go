package proxy

import (
	"context"
	"errors"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// NewAllocator builds the production ForwardAllocator: it opens a dual-stack
// Local Endpoint through the live Forwarding Session Dialer.
func NewAllocator(dialer core.Dialer) core.ForwardAllocator {
	return allocator{dialer: dialer}
}

type allocator struct {
	dialer core.Dialer
}

type ownedForward struct {
	spec     core.ForwardSpec
	endpoint *Endpoint
}

func (a allocator) Allocate(ctx context.Context, spec core.ForwardSpec) (core.OwnedForward, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := OpenEndpoint(EndpointOptions{
		PreferredPort: spec.PreferredLocalPort,
		Remote:        spec.Remote,
		Dialer:        a.dialer,
	})
	if err != nil {
		if errors.Is(err, ErrLocalPortConflict) {
			return nil, &core.DomainError{Kind: core.ErrorLocalPortConflict, Retryable: true}
		}
		return nil, err
	}
	owner := &ownedForward{spec: spec, endpoint: endpoint}
	if err := ctx.Err(); err != nil {
		_ = owner.Close(ctx)
		return nil, err
	}
	return owner, nil
}

func (f *ownedForward) Projection() core.ForwardSnapshot {
	families := []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6}
	family := core.FamilyIPv4
	if f.spec.Remote.Addr().Is6() {
		family = core.FamilyIPv6
	}
	return core.ForwardSnapshot{
		ID:                 f.spec.ID,
		RemotePort:         f.spec.Remote.Port(),
		RemoteFamily:       family,
		AllocatedLocalPort: f.endpoint.LocalPort(),
		LocalFamilies:      families,
	}
}

func (f *ownedForward) Close(ctx context.Context) error {
	return f.endpoint.Close(ctx)
}
