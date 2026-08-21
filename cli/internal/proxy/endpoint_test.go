package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type unusedDialer struct{}

func (unusedDialer) DialContext(context.Context, netip.AddrPort) (core.HalfCloseConn, error) {
	return nil, errors.New("unexpected dial")
}

func TestOpenEndpointBindsBothLocalFamiliesAtPreferredPort(t *testing.T) {
	preferred := availablePort(t)
	endpoint, err := openEndpoint(EndpointOptions{
		PreferredPort: preferred,
		Remote:        netip.MustParseAddrPort("127.0.0.1:8080"),
		Dialer:        unusedDialer{},
	})
	if err != nil {
		t.Fatalf("OpenEndpoint: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := endpoint.Close(ctx); err != nil {
			t.Errorf("close Endpoint: %v", err)
		}
	})
	if got := endpoint.LocalPort(); got != preferred {
		t.Fatalf("LocalPort = %d, want preferred port %d", got, preferred)
	}

	for _, address := range []string{
		net.JoinHostPort("127.0.0.1", portText(preferred)),
		net.JoinHostPort("::1", portText(preferred)),
	} {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatalf("connect to Local Endpoint %s: %v", address, err)
		}
		_ = connection.Close()
	}
}

func TestOpenEndpointFailsWhenPreferredPortIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy preferred port: %v", err)
	}
	defer occupied.Close()
	preferred := uint16(occupied.Addr().(*net.TCPAddr).Port)
	endpoint, err := openEndpoint(EndpointOptions{
		PreferredPort: preferred,
		Remote:        netip.MustParseAddrPort("127.0.0.1:8080"),
		Dialer:        unusedDialer{},
	})
	if endpoint != nil {
		t.Cleanup(func() { _ = endpoint.Close(context.Background()) })
		t.Fatal("OpenEndpoint unexpectedly bound an occupied port")
	}
	if err == nil {
		t.Fatal("OpenEndpoint error = nil, want a bind failure")
	}
}

func TestAllocatorFallsBackWhenPreferredPortIsOccupied(t *testing.T) {
	occupied, preferred := occupyPortWithFreeSuccessor(t)
	defer occupied.Close()
	owner, err := NewAllocator(unusedDialer{}).Allocate(context.Background(), core.ForwardSpec{
		ID:                 "managed:test",
		Remote:             netip.MustParseAddrPort("127.0.0.1:8080"),
		PreferredLocalPort: preferred,
	})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := owner.Close(ctx); err != nil {
			t.Errorf("close owner: %v", err)
		}
	})
	if got, want := owner.Projection().AllocatedLocalPort, preferred+1; got != want {
		t.Fatalf("AllocatedLocalPort = %d, want fallback port %d", got, want)
	}
}

func TestAllocatorReturnsConflictAfterLastValidPort(t *testing.T) {
	const preferred uint16 = 65530
	occupied := make([]net.Listener, 0, 6)
	for port := preferred; ; port++ {
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", portText(port)))
		if err != nil {
			for _, opened := range occupied {
				_ = opened.Close()
			}
			t.Skipf("high-port conflict range unavailable: %v", err)
		}
		occupied = append(occupied, listener)
		if port == 65535 {
			break
		}
	}
	for _, listener := range occupied {
		defer listener.Close()
	}

	owner, err := NewAllocator(unusedDialer{}).Allocate(context.Background(), core.ForwardSpec{
		ID:                 "managed:test",
		Remote:             netip.MustParseAddrPort("127.0.0.1:8080"),
		PreferredLocalPort: preferred,
	})
	if owner != nil {
		t.Cleanup(func() { _ = owner.Close(context.Background()) })
		t.Fatal("Allocate unexpectedly returned an owner")
	}
	var domain *core.DomainError
	if !errors.As(err, &domain) || domain.Kind != core.ErrorLocalPortConflict {
		t.Fatalf("Allocate error = %v, want ErrorLocalPortConflict", err)
	}
}

func availablePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve preferred port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release preferred port: %v", err)
	}
	return uint16(port)
}

func occupyPortWithFreeSuccessor(t *testing.T) (net.Listener, uint16) {
	t.Helper()
	for range 100 {
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("occupy preferred port: %v", err)
		}
		port := occupied.Addr().(*net.TCPAddr).Port
		if port >= 65535 {
			_ = occupied.Close()
			continue
		}
		ipv4, err4 := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1)))
		ipv6, err6 := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port+1)))
		if err4 == nil && err6 == nil {
			_ = ipv4.Close()
			_ = ipv6.Close()
			return occupied, uint16(port)
		}
		if ipv4 != nil {
			_ = ipv4.Close()
		}
		if ipv6 != nil {
			_ = ipv6.Close()
		}
		_ = occupied.Close()
	}
	t.Fatal("could not find occupied port with a free successor")
	return nil, 0
}

func portText(port uint16) string {
	return strconv.Itoa(int(port))
}
