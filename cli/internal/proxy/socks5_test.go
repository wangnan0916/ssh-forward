package proxy_test

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"

	"github.com/google/go-cmp/cmp"
)

func TestSOCKS5DialerConnectsToIPv4AndPreservesHalfClose(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as SOCKS server: %v", err)
	}
	defer listener.Close()
	requests := make(chan []byte, 2)
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
		requests <- greeting
		if _, err := connection.Write([]byte{5, 0}); err != nil {
			serverDone <- err
			return
		}
		connect := make([]byte, 10)
		if _, err := io.ReadFull(connection, connect); err != nil {
			serverDone <- err
			return
		}
		requests <- connect
		if _, err := connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			serverDone <- err
			return
		}
		_, err = io.Copy(io.Discard, connection)
		serverDone <- err
	}()

	serverAddress := listener.Addr().(*net.TCPAddr).AddrPort()
	dialer := proxy.NewSOCKS5Dialer(serverAddress)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := dialer.DialContext(ctx, netip.MustParseAddrPort("127.0.0.1:8080"))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close SOCKS connection: %v", err)
	}

	wantRequests := [][]byte{
		{5, 1, 0},
		{5, 1, 0, 1, 127, 0, 0, 1, 0x1f, 0x90},
	}
	for _, want := range wantRequests {
		select {
		case got := <-requests:
			if diff := cmp.Diff(got, want); diff != "" {
				t.Fatalf("SOCKS request mismatch (-got +want):\n%s", diff)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for SOCKS request")
		}
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
