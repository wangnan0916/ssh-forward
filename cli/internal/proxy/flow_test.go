package proxy_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

type directDialer struct{}

func (directDialer) DialContext(ctx context.Context, target netip.AddrPort) (proxy.HalfCloseConn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", target.String())
	if err != nil {
		return nil, err
	}
	tcpConnection, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("direct connection type %T is not TCP", connection)
	}
	return tcpConnection, nil
}

func TestEndpointPreservesResponseAfterClientHalfClose(t *testing.T) {
	remote, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as remote fixture: %v", err)
	}
	defer remote.Close()
	fixtureDone := make(chan error, 1)
	go func() {
		connection, err := remote.Accept()
		if err != nil {
			fixtureDone <- err
			return
		}
		defer connection.Close()
		request, err := io.ReadAll(connection)
		if err != nil {
			fixtureDone <- err
			return
		}
		_, err = connection.Write(append([]byte("response:"), request...))
		fixtureDone <- err
	}()

	preferred := availablePort(t)
	endpoint, err := proxy.OpenEndpoint(proxy.EndpointOptions{
		PreferredPort: preferred,
		Remote:        remote.Addr().(*net.TCPAddr).AddrPort(),
		Dialer:        directDialer{},
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
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set Local Endpoint deadline: %v", err)
	}
	if _, err := connection.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-close request: %v", err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got, want := string(response), "response:request"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	select {
	case err := <-fixtureDone:
		if err != nil {
			t.Fatalf("remote fixture: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote fixture did not stop")
	}
}
