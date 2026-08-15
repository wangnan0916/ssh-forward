package proxy_test

import (
	"context"
	"io"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"
)

func TestSOCKS5DialerConnectsToIPv6Loopback(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as SOCKS server: %v", err)
	}
	defer listener.Close()
	connectRequest := make(chan []byte, 1)
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(connection, greeting); err != nil {
			serverDone <- err
			return
		}
		if _, err := connection.Write([]byte{5, 0}); err != nil {
			serverDone <- err
			return
		}
		connect := make([]byte, 22)
		if _, err := io.ReadFull(connection, connect); err != nil {
			serverDone <- err
			return
		}
		connectRequest <- connect
		_, err = connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		serverDone <- err
	}()

	dialer := proxy.NewSOCKS5Dialer(listener.Addr().(*net.TCPAddr).AddrPort())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := dialer.DialContext(ctx, netip.MustParseAddrPort("[::1]:8080"))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = connection.Close()

	want := []byte{5, 1, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0x1f, 0x90}
	select {
	case got := <-connectRequest:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("SOCKS CONNECT request = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SOCKS CONNECT request")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("SOCKS server: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS server did not stop")
	}
}
