package core

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type unusedConnector struct{}

func (unusedConnector) Connect(context.Context, HostAlias) (hostSession, error) {
	return nil, errors.New("unexpected Connect call")
}

type blockingConnector struct {
	started chan HostAlias
}

func (c blockingConnector) Connect(ctx context.Context, host HostAlias) (hostSession, error) {
	c.started <- host
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConfiguredManagerStartsDisconnectedWithoutConnecting(t *testing.T) {
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: unusedConnector{},
	})
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := Snapshot{
		Revision: 0,
		Hosts: []HostSnapshot{
			{
				Alias:                HostAlias("development"),
				Connection:           ConnectionDisconnected,
				Discovery:            stoppedDiscovery(),
				ListenerObservations: []ListenerObservation{},
				Forwards:             []ForwardSnapshot{},
			},
		},
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("Snapshot = %#v, want %#v", snapshot, want)
	}
}

func TestAddManualForwardAllocatesEndpointAndConnectsLazily(t *testing.T) {
	connector := blockingConnector{started: make(chan HostAlias, 1)}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: connector,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	preferredPort := freePort(t)
	command := AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: preferredPort,
		Family:     FamilyAuto,
	}
	outcome, err := manager.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute AddManualForward: %v", err)
	}
	wantForward := ForwardSnapshot{
		ID:                 ForwardID("manual:operation-1"),
		Kind:               ForwardManual,
		RemotePort:         preferredPort,
		RemoteFamily:       FamilyIPv4,
		AllocatedLocalPort: preferredPort,
		LocalFamilies:      []AddressFamily{FamilyIPv4, FamilyIPv6},
	}
	wantOutcome := Outcome{
		Kind:     OutcomeForwardAdded,
		Revision: 1,
		Forward:  wantForward,
	}
	if !reflect.DeepEqual(outcome, wantOutcome) {
		t.Fatalf("Outcome = %#v, want %#v", outcome, wantOutcome)
	}
	select {
	case host := <-connector.started:
		if host != HostAlias("development") {
			t.Fatalf("connected host = %q, want development", host)
		}
	case <-time.After(time.Second):
		t.Fatal("Development Host connection did not start")
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	wantSnapshot := Snapshot{
		Revision: 1,
		Hosts: []HostSnapshot{
			{
				Alias:                HostAlias("development"),
				Connection:           ConnectionConnecting,
				Discovery:            stoppedDiscovery(),
				ListenerObservations: []ListenerObservation{},
				Forwards:             []ForwardSnapshot{wantForward},
			},
		},
	}
	if !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("Snapshot = %#v, want %#v", snapshot, wantSnapshot)
	}
}

func TestAddManualForwardHonorsCancellationBeforeCommit(t *testing.T) {
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: unusedConnector{},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := manager.Execute(ctx, AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 0 || len(snapshot.Hosts[0].Forwards) != 0 {
		t.Fatalf("cancelled command changed Snapshot: %#v", snapshot)
	}
}

func TestAddManualForwardReturnsTypedUnknownHostError(t *testing.T) {
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: unusedConnector{},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("other-host"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	})
	var domainError *DomainError
	if !errors.As(err, &domainError) {
		t.Fatalf("Execute error = %v, want DomainError", err)
	}
	if domainError.Kind != ErrorUnknownHost || domainError.Retryable {
		t.Fatalf("DomainError = %#v, want non-retryable unknown_host", domainError)
	}
}

func TestAddManualForwardIsIdempotentByCommandID(t *testing.T) {
	connector := blockingConnector{started: make(chan HostAlias, 1)}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: connector,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	command := AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	}
	first, err := manager.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := manager.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("retried Outcome = %#v, want original %#v", second, first)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 1 || len(snapshot.Hosts[0].Forwards) != 1 {
		t.Fatalf("retried Snapshot = %#v, want revision 1 with one Forward", snapshot)
	}
}

func TestCommandIDConflictPrecedesValidationOfReusedInput(t *testing.T) {
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: blockingConnector{started: make(chan HostAlias, 1)},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if _, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	_, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("other-host"),
		RemotePort: 0,
		Family:     AddressFamily("unknown"),
	})
	var domainError *DomainError
	if !errors.As(err, &domainError) || domainError.Kind != ErrorCommandIDConflict {
		t.Fatalf("reused CommandID error = %v, want command_id_conflict", err)
	}
}

func TestConcurrentIdenticalCommandsShareOneOutcome(t *testing.T) {
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: blockingConnector{started: make(chan HostAlias, 1)},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	command := AddManualForward{
		CommandID:  CommandID("operation-1"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	}
	type result struct {
		outcome Outcome
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			outcome, err := manager.Execute(context.Background(), command)
			results <- result{outcome: outcome, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent Execute errors = %v, %v", first.err, second.err)
	}
	if !reflect.DeepEqual(first.outcome, second.outcome) {
		t.Fatalf("concurrent Outcomes differ: %#v and %#v", first.outcome, second.outcome)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 1 || len(snapshot.Hosts[0].Forwards) != 1 {
		t.Fatalf("concurrent Snapshot = %#v, want revision 1 with one Forward", snapshot)
	}
}

func TestRemoveForwardHonorsCancellationBeforeCommit(t *testing.T) {
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: blockingConnector{started: make(chan HostAlias, 1)},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	added, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = manager.Execute(ctx, RemoveForward{
		CommandID: CommandID("operation-remove"),
		ForwardID: added.Forward.ID,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("remove error = %v, want context.Canceled", err)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 1 || len(snapshot.Hosts[0].Forwards) != 1 {
		t.Fatalf("cancelled removal changed Snapshot: %#v", snapshot)
	}
}

func TestRemoveManualForwardReleasesLocalEndpoint(t *testing.T) {
	connector := blockingConnector{started: make(chan HostAlias, 1)}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: connector,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	added, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: freePort(t),
		Family:     FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	removed, err := manager.Execute(context.Background(), RemoveForward{
		CommandID: CommandID("operation-remove"),
		ForwardID: added.Forward.ID,
	})
	if err != nil {
		t.Fatalf("remove Manual Forward: %v", err)
	}
	wantOutcome := Outcome{
		Kind:     OutcomeForwardRemoved,
		Revision: 2,
		Forward:  added.Forward,
	}
	if !reflect.DeepEqual(removed, wantOutcome) {
		t.Fatalf("remove Outcome = %#v, want %#v", removed, wantOutcome)
	}
	snapshot, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 2 || len(snapshot.Hosts[0].Forwards) != 0 {
		t.Fatalf("Snapshot after removal = %#v, want revision 2 with no Forwards", snapshot)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(added.Forward.AllocatedLocalPort)))
	connection, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("removed Local Endpoint still accepts connections")
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release local port: %v", err)
	}
	return uint16(port)
}
