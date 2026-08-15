package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"
)

type delayedDialSession struct {
	*directHostSession
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (s *delayedDialSession) DialContext(context.Context, netip.AddrPort) (proxy.HalfCloseConn, error) {
	close(s.started)
	<-s.release
	return nil, errors.New("dial released after Endpoint shutdown")
}

func (s *delayedDialSession) releaseDial() {
	s.releaseOnce.Do(func() { close(s.release) })
}

type removeResult struct {
	outcome Outcome
	err     error
}

func TestIdempotentRemoveWaitsUntilEndpointStops(t *testing.T) {
	manager, session, added, connection := setupDelayedRemoval(t)
	command := RemoveForward{CommandID: CommandID("operation-remove"), ForwardID: added.Forward.ID}
	firstDone := executeRemove(manager, command)
	waitForEndpointStop(t, connection)
	retryDone := executeRemove(manager, command)
	assertRemoveStillWaiting(t, retryDone)

	session.releaseDial()
	first := <-firstDone
	retry := <-retryDone
	if first.err != nil || retry.err != nil {
		t.Fatalf("remove errors = %v and %v", first.err, retry.err)
	}
	if !reflect.DeepEqual(first.outcome, retry.outcome) {
		t.Fatalf("remove Outcomes differ: %#v and %#v", first.outcome, retry.outcome)
	}
}

func TestCancelledRemoveFinishesCommittedShutdownInBackground(t *testing.T) {
	manager, session, added, connection := setupDelayedRemoval(t)
	ctx, cancel := context.WithCancel(context.Background())
	command := RemoveForward{CommandID: CommandID("operation-remove"), ForwardID: added.Forward.ID}
	done := make(chan removeResult, 1)
	go func() {
		outcome, err := manager.Execute(ctx, command)
		done <- removeResult{outcome: outcome, err: err}
	}()
	waitForEndpointStop(t, connection)
	cancel()
	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled remove error = %v, want context.Canceled", result.err)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Hosts[0].Forwards) != 1 {
		t.Fatalf("removal published before Endpoint workers stopped: %#v", snapshot)
	}

	session.releaseDial()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err = manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Hosts[0].Forwards) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background removal did not publish")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := manager.Execute(context.Background(), command); err != nil {
		t.Fatalf("idempotent retry after background removal: %v", err)
	}
}

func TestConcurrentDifferentRemoveWaitsForConsistentSnapshot(t *testing.T) {
	manager, session, added, connection := setupDelayedRemoval(t)
	firstDone := executeRemove(manager, RemoveForward{
		CommandID: CommandID("operation-remove-1"),
		ForwardID: added.Forward.ID,
	})
	waitForEndpointStop(t, connection)
	secondDone := executeRemove(manager, RemoveForward{
		CommandID: CommandID("operation-remove-2"),
		ForwardID: added.Forward.ID,
	})
	assertRemoveStillWaiting(t, secondDone)

	session.releaseDial()
	if result := <-firstDone; result.err != nil {
		t.Fatalf("first remove: %v", result.err)
	}
	second := <-secondDone
	var domainError *DomainError
	if !errors.As(second.err, &domainError) || domainError.Kind != ErrorForwardNotFound {
		t.Fatalf("second remove error = %v, want forward_not_found", second.err)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Hosts[0].Forwards) != 0 {
		t.Fatalf("Snapshot after forward_not_found still contains Forward: %#v", snapshot)
	}
}

func setupDelayedRemoval(t *testing.T) (*manager, *delayedDialSession, Outcome, net.Conn) {
	t.Helper()
	session := &delayedDialSession{
		directHostSession: newDirectHostSession(),
		started:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: immediateConnector{session: session},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	t.Cleanup(session.releaseDial)
	added, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	waitForConnectionState(t, manager, ConnectionConnected, 2)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(added.Forward.AllocatedLocalPort)))
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("connect to Local Endpoint: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("proxied dial did not start")
	}
	return manager, session, added, connection
}

func executeRemove(manager Manager, command RemoveForward) <-chan removeResult {
	done := make(chan removeResult, 1)
	go func() {
		outcome, err := manager.Execute(context.Background(), command)
		done <- removeResult{outcome: outcome, err: err}
	}()
	return done
}

func waitForEndpointStop(t *testing.T, connection net.Conn) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set Local Endpoint deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("Local Endpoint remained open after removal started")
	}
}

func assertRemoveStillWaiting(t *testing.T, done <-chan removeResult) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("remove returned before Endpoint stopped: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
}
