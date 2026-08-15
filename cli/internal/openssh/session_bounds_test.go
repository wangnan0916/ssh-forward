package openssh

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"ssh-forward/cli/internal/proxy"
)

var errUnexpectedSessionDial = errors.New("underlying dialer was called")

type countingSessionDialer struct {
	calls int
}

func (d *countingSessionDialer) DialContext(context.Context, netip.AddrPort) (proxy.HalfCloseConn, error) {
	d.calls++
	return nil, errUnexpectedSessionDial
}

func TestSessionRejectsTargetsOutsideRemoteLoopback(t *testing.T) {
	targets := []netip.AddrPort{
		{},
		netip.MustParseAddrPort("127.0.0.1:0"),
		netip.MustParseAddrPort("192.0.2.1:8080"),
		netip.MustParseAddrPort("[2001:db8::1]:8080"),
	}
	for _, target := range targets {
		dialer := &countingSessionDialer{}
		session := &Session{dialer: dialer}
		if _, err := session.DialContext(context.Background(), target); err == nil {
			t.Fatalf("DialContext(%v) succeeded", target)
		}
		if dialer.calls != 0 {
			t.Fatalf("DialContext(%v) reached underlying SOCKS dialer", target)
		}
	}

	for _, target := range []netip.AddrPort{
		netip.MustParseAddrPort("127.0.0.1:8080"),
		netip.MustParseAddrPort("[::1]:8080"),
	} {
		dialer := &countingSessionDialer{}
		session := &Session{dialer: dialer}
		if _, err := session.DialContext(context.Background(), target); !errors.Is(err, errUnexpectedSessionDial) {
			t.Fatalf("DialContext(%v) error = %v, want underlying dial error", target, err)
		}
		if dialer.calls != 1 {
			t.Fatalf("DialContext(%v) underlying calls = %d, want 1", target, dialer.calls)
		}
	}
}
