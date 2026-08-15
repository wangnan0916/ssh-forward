package core

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	"ssh-forward/cli/internal/proxy"
)

type scriptedDiscoverySession struct {
	facts     chan SessionFact
	terminal  chan error
	closed    chan struct{}
	closeOnce sync.Once
}

func newScriptedDiscoverySession() *scriptedDiscoverySession {
	return &scriptedDiscoverySession{
		facts:    make(chan SessionFact, 4),
		terminal: make(chan error, 1),
		closed:   make(chan struct{}),
	}
}

func (*scriptedDiscoverySession) DialContext(context.Context, netip.AddrPort) (proxy.HalfCloseConn, error) {
	return nil, errors.New("unexpected Forward dial")
}

func (s *scriptedDiscoverySession) Next(ctx context.Context) (SessionFact, error) {
	select {
	case fact := <-s.facts:
		return fact, nil
	case err := <-s.terminal:
		return nil, err
	case <-s.closed:
		return nil, &SessionError{Disposition: SessionClosed, Reason: SessionReasonClosed}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *scriptedDiscoverySession) Close(context.Context) error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type oneSessionConnector struct {
	session hostSession
}

func (c oneSessionConnector) Connect(context.Context, HostAlias) (hostSession, error) {
	return c.session, nil
}

func TestManagerRetainsObservationsUntilReconnectGetsCompleteReplacement(t *testing.T) {
	first := newScriptedDiscoverySession()
	second := newScriptedDiscoverySession()
	ready := make(chan struct{})
	close(ready)
	connector := &sequenceConnector{
		sessions: []hostSession{first, second},
		releases: []<-chan struct{}{ready, ready},
		started:  make(chan int, 2),
	}
	owner := &scriptedOwnedForward{
		projection: ForwardSnapshot{
			ID:                 ForwardID("manual:operation-add"),
			Kind:               ForwardManual,
			RemotePort:         8080,
			RemoteFamily:       FamilyIPv4,
			AllocatedLocalPort: 8087,
			LocalFamilies:      []AddressFamily{FamilyIPv4, FamilyIPv6},
		},
		closeStart: make(chan struct{}),
		closeDone:  make(chan struct{}),
	}
	manager := newManager(managerOptions{
		host:       HostAlias("development"),
		connector:  connector,
		retryDelay: func(int) time.Duration { return 0 },
		retryWait: func(ctx context.Context, _ time.Duration) bool {
			return ctx.Err() == nil
		},
		forwardAllocator: scriptedForwardAllocator{
			requests: make(chan forwardSpec, 1),
			owner:    owner,
		},
	})
	t.Cleanup(func() {
		owner.release()
		_ = manager.Close(context.Background())
	})
	if _, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	waitForDiscoveryState(t, manager, DiscoveryStarting)
	observation := ListenerObservation{
		Family:           FamilyIPv4,
		BindScope:        BindLoopback,
		RemotePort:       38080,
		SocketIdentities: []SocketIdentity{SocketIdentity("socket:retained")},
		Processes:        []ProcessChain{},
	}
	full := DiscoveryCapability{
		RemoteListeners: CapabilityFull,
		SocketIdentity:  CapabilityFull,
		ProcessMetadata: CapabilityPartial,
	}
	first.facts <- ObservationSet{Sequence: 1, Capability: full, Observations: []ListenerObservation{observation}}
	waitForDiscoveryBaseline(t, manager, true)
	first.terminal <- &SessionError{Disposition: SessionRetry, Reason: SessionReasonTransport}
	starting := waitForDiscoveryState(t, manager, DiscoveryStarting)
	if got := starting.Hosts[0].ListenerObservations; !reflect.DeepEqual(got, []ListenerObservation{observation}) {
		t.Fatalf("reconnect discarded retained observations: %#v", got)
	}
	partial := DiscoveryCapability{
		RemoteListeners: CapabilityPartial,
		SocketIdentity:  CapabilityPartial,
		ProcessMetadata: CapabilityUnavailable,
	}
	second.facts <- ObservationSet{Sequence: 1, Capability: partial}
	degraded := waitForDiscoveryCapability(t, manager, partial)
	if degraded.Hosts[0].Discovery.BaselineEstablished {
		t.Fatalf("partial reconnect established baseline: %#v", degraded.Hosts[0].Discovery)
	}
	if got := degraded.Hosts[0].ListenerObservations; !reflect.DeepEqual(got, []ListenerObservation{observation}) {
		t.Fatalf("partial reconnect replaced retained observations: %#v", got)
	}
}

func TestManagerPublishesDiscoveryBaselineAtomically(t *testing.T) {
	session := newScriptedDiscoverySession()
	owner := &scriptedOwnedForward{
		projection: ForwardSnapshot{
			ID:                 ForwardID("manual:operation-add"),
			Kind:               ForwardManual,
			RemotePort:         8080,
			RemoteFamily:       FamilyIPv4,
			AllocatedLocalPort: 8087,
			LocalFamilies:      []AddressFamily{FamilyIPv4, FamilyIPv6},
		},
		closeStart: make(chan struct{}),
		closeDone:  make(chan struct{}),
	}
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: oneSessionConnector{session: session},
		forwardAllocator: scriptedForwardAllocator{
			requests: make(chan forwardSpec, 1),
			owner:    owner,
		},
	})
	t.Cleanup(func() {
		owner.release()
		_ = manager.Close(context.Background())
	})
	if _, err := manager.Execute(context.Background(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	starting := waitForDiscoveryState(t, manager, DiscoveryStarting)
	stream, err := manager.Watch(context.Background(), WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	initial, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("initial Next: %v", err)
	}
	if !reflect.DeepEqual(initial, starting) {
		t.Fatalf("Watch initial = %#v, want starting Snapshot %#v", initial, starting)
	}

	capability := DiscoveryCapability{
		RemoteListeners: CapabilityFull,
		SocketIdentity:  CapabilityFull,
		ProcessMetadata: CapabilityPartial,
	}
	observation := ListenerObservation{
		Family:           FamilyIPv4,
		BindScope:        BindLoopback,
		RemotePort:       38080,
		SocketIdentities: []SocketIdentity{SocketIdentity("socket:test")},
		Processes: []ProcessChain{{Processes: []ProcessMetadata{{
			PID:              42,
			Executable:       "/usr/bin/python3",
			WorkingDirectory: "/workspace",
			Arguments:        []string{"python3", "fixture.py"},
		}}}},
	}
	session.facts <- ObservationSet{
		Sequence:     1,
		Capability:   capability,
		Observations: []ListenerObservation{observation},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	baseline, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("baseline Next: %v", err)
	}
	if baseline.Revision != starting.Revision+1 {
		t.Fatalf("baseline revision = %d, want %d", baseline.Revision, starting.Revision+1)
	}
	if got := baseline.Hosts[0].Discovery; got.State != DiscoveryDegraded || !got.BaselineEstablished || !reflect.DeepEqual(got.Capability, capability) {
		t.Fatalf("Discovery = %#v, want atomic degraded baseline with %#v", got, capability)
	}
	if got := baseline.Hosts[0].ListenerObservations; !reflect.DeepEqual(got, []ListenerObservation{observation}) {
		t.Fatalf("Listener Observations = %#v, want %#v", got, []ListenerObservation{observation})
	}
	baseline.Hosts[0].ListenerObservations[0].Processes[0].Processes[0].Arguments[0] = "mutated"
	immutable, err := manager.Snapshot(context.Background(), AllHosts())
	if err != nil {
		t.Fatalf("Snapshot after caller mutation: %v", err)
	}
	if got := immutable.Hosts[0].ListenerObservations[0].Processes[0].Processes[0].Arguments[0]; got != "python3" {
		t.Fatalf("caller mutation changed canonical Process Metadata: %q", got)
	}

	partialCapability := DiscoveryCapability{
		RemoteListeners: CapabilityPartial,
		SocketIdentity:  CapabilityPartial,
		ProcessMetadata: CapabilityUnavailable,
	}
	partialObservation := ListenerObservation{
		Family:           FamilyIPv4,
		BindScope:        BindLoopback,
		RemotePort:       38080,
		SocketIdentities: []SocketIdentity{SocketIdentity("socket:new")},
		Processes: []ProcessChain{{Processes: []ProcessMetadata{{
			PID:        43,
			Executable: "/usr/bin/new-owner",
		}}}},
	}
	session.facts <- ObservationSet{Sequence: 2, Capability: partialCapability, Observations: []ListenerObservation{partialObservation}}
	partial, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("partial Next: %v", err)
	}
	if got := partial.Hosts[0].Discovery; got.State != DiscoveryDegraded || !got.BaselineEstablished || !reflect.DeepEqual(got.Capability, partialCapability) {
		t.Fatalf("partial Discovery = %#v, want degraded retained baseline with %#v", got, partialCapability)
	}
	merged := partial.Hosts[0].ListenerObservations
	if len(merged) != 1 || !reflect.DeepEqual(merged[0].SocketIdentities, []SocketIdentity{SocketIdentity("socket:new"), SocketIdentity("socket:test")}) || len(merged[0].Processes) != 2 {
		t.Fatalf("partial observation did not merge retained and current evidence: %#v", merged)
	}

	session.facts <- ObservationSet{Sequence: 2, Capability: capability}
	failed, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("invalid fact Next: %v", err)
	}
	if got := failed.Hosts[0]; got.Connection != ConnectionConnected || got.Discovery.State != DiscoveryFailed || got.Discovery.Diagnostic != "invalid_session_fact" {
		t.Fatalf("invalid discovery fact disrupted Forwarding Session: %#v", got)
	}
}

func waitForDiscoveryBaseline(t *testing.T, manager Manager, established bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Hosts) == 1 && snapshot.Hosts[0].Discovery.BaselineEstablished == established {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Discovery baseline did not become %t; last Snapshot: %#v", established, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForDiscoveryCapability(t *testing.T, manager Manager, capability DiscoveryCapability) Snapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Hosts) == 1 && reflect.DeepEqual(snapshot.Hosts[0].Discovery.Capability, capability) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Discovery Capability did not become %#v; last Snapshot: %#v", capability, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForDiscoveryState(t *testing.T, manager Manager, state DiscoveryState) Snapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background(), AllHosts())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snapshot.Hosts) == 1 && snapshot.Hosts[0].Discovery.State == state {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Discovery did not reach %q; last Snapshot: %#v", state, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}
