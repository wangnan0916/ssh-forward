package core

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

type sequenceConnector struct {
	mu       sync.Mutex
	next     int
	sessions []hostSession
	releases []<-chan struct{}
	started  chan int
}

func (c *sequenceConnector) Connect(ctx context.Context, _ HostAlias) (hostSession, error) {
	c.mu.Lock()
	index := c.next
	c.next++
	c.mu.Unlock()
	c.started <- index
	select {
	case <-c.releases[index]:
		return c.sessions[index], nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type permanentFailureConnector struct {
	started chan struct{}
}

func (c permanentFailureConnector) Connect(context.Context, HostAlias) (hostSession, error) {
	close(c.started)
	return nil, permanentConnectionError{err: errors.New("authentication failed")}
}

func TestManagerSuspendsReconnectAfterPermanentSSHFailure(t *testing.T) {
	connector := permanentFailureConnector{started: make(chan struct{})}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: connector,
		retryWait: func(context.Context, time.Duration) bool {
			t.Fatal("permanent failure unexpectedly entered retry backoff")
			return false
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if _, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	select {
	case <-connector.started:
	case <-time.After(time.Second):
		t.Fatal("connection attempt did not start")
	}
	waitForConnectionState(t, manager, ConnectionDisconnected, 2)
}

func TestManualForwardRetainsEndpointAcrossSessionReplacement(t *testing.T) {
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
		_, err = connection.Write(append([]byte("replacement:"), request...))
		fixtureDone <- err
	}()

	firstReady := make(chan struct{})
	close(firstReady)
	secondReady := make(chan struct{})
	firstSession := newDirectHostSession()
	secondSession := newDirectHostSession()
	connector := &sequenceConnector{
		sessions: []hostSession{firstSession, secondSession},
		releases: []<-chan struct{}{firstReady, secondReady},
		started:  make(chan int, 2),
	}
	manager := newManager(managerOptions{
		host:       HostAlias("development"),
		connector:  connector,
		retryDelay: func(int) time.Duration { return 0 },
		retryWait: func(ctx context.Context, _ time.Duration) bool {
			return ctx.Err() == nil
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	added, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: uint16(remote.Addr().(*net.TCPAddr).Port),
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	waitForConnectionState(t, manager, ConnectionConnected, 2)
	if err := firstSession.Close(context.Background()); err != nil {
		t.Fatalf("end first Session: %v", err)
	}
	select {
	case attempt := <-connector.started:
		if attempt != 0 {
			t.Fatalf("first connection attempt = %d, want 0", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("initial connection attempt did not start")
	}
	select {
	case attempt := <-connector.started:
		if attempt != 1 {
			t.Fatalf("replacement connection attempt = %d, want 1", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement connection attempt did not start")
	}
	snapshot := waitForConnectionState(t, manager, ConnectionConnecting, 3)
	if got := snapshot.Hosts[0].Forwards[0].AllocatedLocalPort; got != added.Forward.AllocatedLocalPort {
		t.Fatalf("retained Local Port = %d, want %d", got, added.Forward.AllocatedLocalPort)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(added.Forward.AllocatedLocalPort)))
	disconnected, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("connect to retained Endpoint: %v", err)
	}
	if err := disconnected.SetDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("set disconnected deadline: %v", err)
	}
	_, _ = disconnected.Write([]byte("request"))
	buffer := make([]byte, 1)
	if _, err := disconnected.Read(buffer); err == nil {
		t.Fatal("new connection did not fail while Session was disconnected")
	}
	_ = disconnected.Close()

	close(secondReady)
	waitForConnectionState(t, manager, ConnectionConnected, 4)
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("connect after replacement: %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set replacement deadline: %v", err)
	}
	if _, err := connection.Write([]byte("request")); err != nil {
		t.Fatalf("write through replacement: %v", err)
	}
	if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-close replacement request: %v", err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read replacement response: %v", err)
	}
	if got, want := string(response), "replacement:request"; got != want {
		t.Fatalf("replacement response = %q, want %q", got, want)
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

func waitForConnectionState(t *testing.T, manager Manager, state ConnectionState, revision Revision) Snapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Hosts[0].Connection == state && snapshot.Revision == revision {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Snapshot = %#v, want connection %q at revision %d", snapshot, state, revision)
		}
		time.Sleep(time.Millisecond)
	}
}
