package proxy

import (
	"context"
	"errors"
	"syscall"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// fallbackPortRoom is the ADR-0008 bounded fallback width: allocation tries
// the Preferred Local Port, then each successor up to +fallbackPortRoom.
const fallbackPortRoom = 100

// NewAllocator builds the production ForwardAllocator: it opens a dual-stack
// Local Endpoint through the live Forwarding Session Dialer, applying the
// Local Port Conflict policy.
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
	for _, port := range allocationPorts(spec.PreferredLocalPort) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		endpoint, err := openEndpoint(EndpointOptions{
			PreferredPort: port,
			Remote:        spec.Remote,
			Dialer:        a.dialer,
		})
		if err == nil {
			owner := &ownedForward{spec: spec, endpoint: endpoint}
			if err := ctx.Err(); err != nil {
				_ = owner.Close(ctx)
				return nil, err
			}
			return owner, nil
		}
		if !isAddrInUse(err) {
			return nil, err
		}
	}
	return nil, &core.DomainError{Kind: core.ErrorLocalPortConflict, Retryable: true}
}

func allocationPorts(preferred uint16) []uint16 {
	if preferred == 0 {
		return nil
	}
	last := min(int(preferred)+fallbackPortRoom, 65535)
	ports := make([]uint16, 0, last-int(preferred)+1)
	for port := int(preferred); port <= last; port++ {
		ports = append(ports, uint16(port))
	}
	return ports
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
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
