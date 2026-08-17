package proxy_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

func TestEndpointBoundsPostHalfCloseDrain(t *testing.T) {
	remote, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as remote fixture: %v", err)
	}
	defer remote.Close()
	requestEOF := make(chan struct{})
	fixtureDone := make(chan struct{})
	go func() {
		defer close(fixtureDone)
		connection, err := remote.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = io.ReadAll(connection)
		close(requestEOF)
		for {
			if _, err := connection.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	endpoint, err := proxy.OpenEndpoint(proxy.EndpointOptions{
		PreferredPort: availablePort(t),
		Remote:        remote.Addr().(*net.TCPAddr).AddrPort(),
		Dialer:        directDialer{},
		DrainTimeout:  50 * time.Millisecond,
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
	if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-close request: %v", err)
	}
	select {
	case <-requestEOF:
	case <-time.After(time.Second):
		t.Fatal("remote fixture did not receive request EOF")
	}
	if err := connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	started := time.Now()
	_, err = io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read until drain deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("connection closed after %v, before configured drain period", elapsed)
	}
	select {
	case <-fixtureDone:
	case <-time.After(time.Second):
		t.Fatal("remote connection remained open after drain deadline")
	}
}
