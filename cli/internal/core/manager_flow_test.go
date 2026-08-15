package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"
)

type directHostSession struct {
	done      chan struct{}
	closeOnce sync.Once
}

func newDirectHostSession() *directHostSession {
	return &directHostSession{done: make(chan struct{})}
}

func (*directHostSession) DialContext(ctx context.Context, target netip.AddrPort) (proxy.HalfCloseConn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", target.String())
	if err != nil {
		return nil, err
	}
	tcpConnection, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("connection type %T is not TCP", connection)
	}
	return tcpConnection, nil
}

func (s *directHostSession) Done() <-chan struct{} {
	return s.done
}

func (s *directHostSession) Close(context.Context) error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type immediateConnector struct {
	session hostSession
}

func (c immediateConnector) Connect(context.Context, HostAlias) (hostSession, error) {
	return c.session, nil
}

func TestManualForwardCarriesTrafficAfterHostConnects(t *testing.T) {
	remote, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as Development Host fixture: %v", err)
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
		_, err = connection.Write(append([]byte("remote:"), request...))
		fixtureDone <- err
	}()

	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: immediateConnector{session: newDirectHostSession()},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	outcome, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: uint16(remote.Addr().(*net.TCPAddr).Port),
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("Execute AddManualForward: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Hosts[0].Connection == ConnectionConnected {
			if snapshot.Revision != 2 {
				t.Fatalf("connected Snapshot revision = %d, want 2", snapshot.Revision)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Development Host did not become connected")
		}
		time.Sleep(time.Millisecond)
	}

	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(outcome.Forward.AllocatedLocalPort))
	connection, err := net.DialTimeout("tcp4", address, time.Second)
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
	if got, want := string(response), "remote:request"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	select {
	case err := <-fixtureDone:
		if err != nil {
			t.Fatalf("Development Host fixture: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Development Host fixture did not stop")
	}
}
