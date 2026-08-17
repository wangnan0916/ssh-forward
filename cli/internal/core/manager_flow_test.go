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

	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

// trafficSession is a host session that dials a local fixture (standing in
// for the Development Host listener) and feeds scripted observations so a
// Managed Forward can be created without a real SSH hop.
type trafficSession struct {
	facts     chan SessionFact
	closed    chan struct{}
	closeOnce sync.Once
	target    string
}

func newTrafficSession(target string) *trafficSession {
	return &trafficSession{
		facts:  make(chan SessionFact, 4),
		closed: make(chan struct{}),
		target: target,
	}
}

func (s *trafficSession) DialContext(ctx context.Context, _ netip.AddrPort) (proxy.HalfCloseConn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.target)
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

func (s *trafficSession) Next(ctx context.Context) (SessionFact, error) {
	select {
	case fact := <-s.facts:
		return fact, nil
	case <-s.closed:
		return nil, &SessionError{Disposition: SessionClosed, Reason: SessionReasonClosed}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *trafficSession) Close(context.Context) error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func TestManagedForwardCarriesTrafficAfterHostConnects(t *testing.T) {
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

	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Preferred Local Port: %v", err)
	}
	port := uint16(probe.Addr().(*net.TCPAddr).Port)
	if err := probe.Close(); err != nil {
		t.Fatalf("release Preferred Local Port: %v", err)
	}

	session := newTrafficSession(remote.Addr().String())
	observation := loopbackListener(port)
	session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{observation}}
	session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability, Budget: fullObservationBudget, Observations: []ListenerObservation{observation}}

	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: oneSessionConnector{session: session},
		policies: func() []ForwardingPolicy {
			return []ForwardingPolicy{{
				ID:         "p1",
				Action:     PolicyAutoForward,
				Conditions: []PolicyCondition{{RemotePorts: policyPort(port)}},
			}}
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	forward := waitForManagedForward(t, manager, port)
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(forward.AllocatedLocalPort))
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

func waitForManagedForward(t *testing.T, manager Manager, port uint16) ForwardSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last Snapshot
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		last = snapshot
		if snapshot.Host != nil {
			for _, forward := range snapshot.Host.Forwards {
				if forward.RemotePort == port {
					return forward
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Managed Forward for port %d did not appear; last Snapshot: %#v", port, last)
		}
		time.Sleep(time.Millisecond)
	}
}
