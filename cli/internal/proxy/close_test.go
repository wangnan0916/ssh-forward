package proxy_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"
)

func TestEndpointCloseTerminatesEstablishedConnections(t *testing.T) {
	remote, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as remote fixture: %v", err)
	}
	defer remote.Close()
	accepted := make(chan struct{})
	fixtureDone := make(chan struct{})
	go func() {
		defer close(fixtureDone)
		connection, err := remote.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, connection)
	}()

	endpoint, err := proxy.OpenEndpoint(proxy.EndpointOptions{
		PreferredPort: availablePort(t),
		Remote:        remote.Addr().(*net.TCPAddr).AddrPort(),
		Dialer:        directDialer{},
	})
	if err != nil {
		t.Fatalf("OpenEndpoint: %v", err)
	}
	connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", portText(endpoint.LocalPort())), time.Second)
	if err != nil {
		t.Fatalf("connect to Local Endpoint: %v", err)
	}
	defer connection.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("remote fixture did not accept proxied connection")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := endpoint.Close(ctx); err != nil {
		t.Fatalf("close Endpoint: %v", err)
	}
	select {
	case <-fixtureDone:
	case <-time.After(time.Second):
		t.Fatal("remote connection remained open after Endpoint close")
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set local read deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("local connection remained usable after Endpoint close")
	}
	probe, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", portText(endpoint.LocalPort())), 100*time.Millisecond)
	if err == nil {
		_ = probe.Close()
		t.Fatal("Local Endpoint still accepted connections after close")
	}
}
