package proxy_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"
)

func TestSOCKS5DialerCancelsIncompleteHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as SOCKS server: %v", err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(connection, greeting); err != nil {
			return
		}
		accepted <- connection
		_, _ = io.Copy(io.Discard, connection)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	dialer := proxy.NewSOCKS5Dialer(listener.Addr().(*net.TCPAddr).AddrPort())
	result := make(chan error, 1)
	go func() {
		connection, err := dialer.DialContext(ctx, netip.MustParseAddrPort("127.0.0.1:8080"))
		if connection != nil {
			_ = connection.Close()
		}
		result <- err
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("SOCKS server did not receive greeting")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DialContext error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("DialContext did not stop after cancellation")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("SOCKS connection remained open after cancellation")
	}
}
