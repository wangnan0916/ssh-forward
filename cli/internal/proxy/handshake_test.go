package proxy_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

type stalledDialer struct {
	started chan struct{}
	result  chan error
}

func (d stalledDialer) DialContext(ctx context.Context, _ netip.AddrPort) (core.HalfCloseConn, error) {
	close(d.started)
	<-ctx.Done()
	d.result <- ctx.Err()
	return nil, ctx.Err()
}

func TestEndpointBoundsSOCKSHandshake(t *testing.T) {
	dialer := stalledDialer{started: make(chan struct{}), result: make(chan error, 1)}
	endpoint, err := proxy.OpenEndpoint(proxy.EndpointOptions{
		PreferredPort:    availablePort(t),
		Remote:           netip.MustParseAddrPort("127.0.0.1:8080"),
		Dialer:           dialer,
		HandshakeTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("OpenEndpoint: %v", err)
	}
	defer endpoint.Close(context.Background())
	connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", portText(endpoint.LocalPort())), time.Second)
	if err != nil {
		t.Fatalf("connect to Local Endpoint: %v", err)
	}
	defer connection.Close()
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("SOCKS handshake did not start")
	}
	if err := connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set Local Endpoint deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("Local Endpoint remained open after handshake deadline")
	}
	select {
	case err := <-dialer.result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("DialContext error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS handshake did not receive its deadline")
	}
}
