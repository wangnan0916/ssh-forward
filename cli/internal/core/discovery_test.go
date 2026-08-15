package core

import (
	"context"
	"errors"
	"fmt"
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

var (
	fullObservationBudget = ObservationBudget{Listeners: 256, Sockets: 256, ProcessRecords: 512, MetadataBytes: 128 << 10}
	fullTestCapability    = DiscoveryCapability{RemoteListeners: CapabilityFull, SocketIdentity: CapabilityFull, ProcessMetadata: CapabilityFull}
)

func TestPartialObservationMergeKeepsFixedEvidenceBounds(t *testing.T) {
	makeListeners := func(firstPort, count int) []ListenerObservation {
		observations := make([]ListenerObservation, count)
		for index := range observations {
			observations[index] = ListenerObservation{
				Family:     FamilyIPv4,
				BindScope:  BindLoopback,
				RemotePort: uint16(firstPort + index),
			}
		}
		return observations
	}
	listeners, listenerTruncation := mergeBoundedListenerObservations(
		makeListeners(1, maxRetainedListenerObservations),
		makeListeners(1+maxRetainedListenerObservations, maxRetainedListenerObservations),
	)
	if len(listeners) > maxRetainedListenerObservations {
		t.Fatalf("retained Listener Observations = %d, want at most %d", len(listeners), maxRetainedListenerObservations)
	}
	if !listenerTruncation.listeners {
		t.Fatal("Listener truncation was not reported")
	}
	retained := makeListeners(1000, maxRetainedListenerObservations)
	withLowerCurrent, _ := mergeBoundedListenerObservations(retained, makeListeners(1, 1))
	if !reflect.DeepEqual(withLowerCurrent, retained) {
		t.Fatal("partial merge evicted retained Listener Observation for a newly observed lower-sorting key")
	}

	makeEvidence := func(first int) ListenerObservation {
		observation := ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080}
		for index := range maxRetainedSocketIdentities {
			identity := first + index
			observation.SocketIdentities = append(observation.SocketIdentities, SocketIdentity(fmt.Sprintf("socket:%04d", identity)))
			observation.Processes = append(observation.Processes, ProcessChain{Processes: []ProcessMetadata{{PID: identity + 1}}})
		}
		return observation
	}
	evidence, truncatedEvidence := mergeBoundedListenerObservations(
		[]ListenerObservation{makeEvidence(0)},
		[]ListenerObservation{makeEvidence(maxRetainedSocketIdentities)},
	)
	if got := len(evidence[0].SocketIdentities); got > maxRetainedSocketIdentities {
		t.Fatalf("retained Socket Identities = %d, want at most %d", got, maxRetainedSocketIdentities)
	}
	if got := len(evidence[0].Processes); got > maxRetainedProcessRecords {
		t.Fatalf("retained Process Chains = %d, want at most %d", got, maxRetainedProcessRecords)
	}
	if !truncatedEvidence.sockets || !truncatedEvidence.processes {
		t.Fatalf("Evidence truncation = %#v, want sockets and processes", truncatedEvidence)
	}
	unavailable := DiscoveryCapability{
		RemoteListeners: CapabilityUnavailable,
		SocketIdentity:  CapabilityUnavailable,
		ProcessMetadata: CapabilityUnavailable,
	}
	degradeTruncatedCapability(&unavailable, evidenceTruncation{listeners: true, sockets: true, processes: true})
	if want := (DiscoveryCapability{RemoteListeners: CapabilityUnavailable, SocketIdentity: CapabilityUnavailable, ProcessMetadata: CapabilityUnavailable}); unavailable != want {
		t.Fatalf("truncation upgraded unavailable Capability: %#v", unavailable)
	}
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
	first.facts <- ObservationSet{Sequence: 1, Capability: full, Observations: []ListenerObservation{observation}, Budget: fullObservationBudget}
	waitForDiscoveryBaseline(t, manager, true)
	first.terminal <- &SessionError{Disposition: SessionRetry, Reason: SessionReasonTransport}
	starting := waitForDiscoveryState(t, manager, DiscoveryStarting)
	if got := starting.Host.ListenerObservations; !reflect.DeepEqual(got, []ListenerObservation{observation}) {
		t.Fatalf("reconnect discarded retained observations: %#v", got)
	}
	partial := DiscoveryCapability{
		RemoteListeners: CapabilityPartial,
		SocketIdentity:  CapabilityPartial,
		ProcessMetadata: CapabilityUnavailable,
	}
	second.facts <- ObservationSet{Sequence: 1, Capability: partial, Budget: fullObservationBudget}
	degraded := waitForDiscoveryCapability(t, manager, partial)
	if degraded.Host.Discovery.BaselineEstablished {
		t.Fatalf("partial reconnect established baseline: %#v", degraded.Host.Discovery)
	}
	if got := degraded.Host.ListenerObservations; !reflect.DeepEqual(got, []ListenerObservation{observation}) {
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
	stream, err := manager.Watch(context.Background())
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
		Budget:       fullObservationBudget,
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
	if got := baseline.Host.Discovery; got.State != DiscoveryDegraded || !got.BaselineEstablished || !reflect.DeepEqual(got.Capability, capability) {
		t.Fatalf("Discovery = %#v, want atomic degraded baseline with %#v", got, capability)
	}
	if got := baseline.Host.ListenerObservations; !reflect.DeepEqual(got, []ListenerObservation{observation}) {
		t.Fatalf("Listener Observations = %#v, want %#v", got, []ListenerObservation{observation})
	}
	baseline.Host.ListenerObservations[0].Processes[0].Processes[0].Arguments[0] = "mutated"
	immutable, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot after caller mutation: %v", err)
	}
	if got := immutable.Host.ListenerObservations[0].Processes[0].Processes[0].Arguments[0]; got != "python3" {
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
	session.facts <- ObservationSet{Sequence: 2, Capability: partialCapability, Observations: []ListenerObservation{partialObservation}, Budget: fullObservationBudget}
	partial, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("partial Next: %v", err)
	}
	if got := partial.Host.Discovery; got.State != DiscoveryDegraded || !got.BaselineEstablished || !reflect.DeepEqual(got.Capability, partialCapability) {
		t.Fatalf("partial Discovery = %#v, want degraded retained baseline with %#v", got, partialCapability)
	}
	merged := partial.Host.ListenerObservations
	if len(merged) != 1 || !reflect.DeepEqual(merged[0].SocketIdentities, []SocketIdentity{SocketIdentity("socket:new"), SocketIdentity("socket:test")}) || len(merged[0].Processes) != 2 {
		t.Fatalf("partial observation did not merge retained and current evidence: %#v", merged)
	}

	boundedObservation := ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 38080}
	for index := range maxRetainedSocketIdentities {
		boundedObservation.SocketIdentities = append(boundedObservation.SocketIdentities, SocketIdentity(fmt.Sprintf("socket:bounded-%03d", index)))
		boundedObservation.Processes = append(boundedObservation.Processes, ProcessChain{Processes: []ProcessMetadata{{PID: 1000 + index}}})
	}
	claimedCapability := DiscoveryCapability{
		RemoteListeners: CapabilityPartial,
		SocketIdentity:  CapabilityFull,
		ProcessMetadata: CapabilityFull,
	}
	session.facts <- ObservationSet{Sequence: 3, Capability: claimedCapability, Observations: []ListenerObservation{boundedObservation}, Budget: fullObservationBudget}
	bounded, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("bounded evidence Next: %v", err)
	}
	if got := bounded.Host.Discovery.Capability; got.SocketIdentity != CapabilityPartial || got.ProcessMetadata != CapabilityPartial {
		t.Fatalf("bounded evidence Capability = %#v, want truncated dimensions partial", got)
	}
	boundedEvidence := bounded.Host.ListenerObservations[0]
	if len(boundedEvidence.SocketIdentities) > maxRetainedSocketIdentities || len(boundedEvidence.Processes) > maxRetainedProcessRecords {
		t.Fatalf("published evidence exceeded bounds: %#v", boundedEvidence)
	}

	session.facts <- ObservationSet{Sequence: 3, Capability: capability, Budget: fullObservationBudget}
	failed, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("invalid fact Next: %v", err)
	}
	if got := failed.Host; got.Connection != ConnectionConnected || got.Discovery.State != DiscoveryFailed || got.Discovery.Diagnostic != "invalid_session_fact" {
		t.Fatalf("invalid discovery fact disrupted Forwarding Session: %#v", got)
	}
}

func TestManagerDegradesDiscoveryOnObservationSequenceGap(t *testing.T) {
	manager, session := newDiscoveryManager(t)
	fullCapability := DiscoveryCapability{
		RemoteListeners: CapabilityFull,
		SocketIdentity:  CapabilityFull,
		ProcessMetadata: CapabilityFull,
	}
	session.facts <- ObservationSet{Sequence: 1, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
	baseline := waitForDiscoveryBaseline(t, manager, true)
	if got := baseline.Host.Discovery.Diagnostic; got != "" {
		t.Fatalf("baseline Diagnostic = %q, want empty", got)
	}

	session.facts <- ObservationSet{Sequence: 3, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
	gapped := waitForDiscoveryState(t, manager, DiscoveryDegraded)
	if got := gapped.Host.Discovery.Diagnostic; got != "observation_resync" {
		t.Fatalf("gap Diagnostic = %q, want observation_resync", got)
	}
	if !gapped.Host.Discovery.BaselineEstablished {
		t.Fatal("sequence gap discarded the established Baseline")
	}

	session.facts <- ObservationSet{Sequence: 4, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
	recovered := waitForDiscoveryState(t, manager, DiscoveryHealthy)
	if got := recovered.Host.Discovery.Diagnostic; got != "" {
		t.Fatalf("recovered Diagnostic = %q, want empty", got)
	}
}

func TestManagerRejectsStaleObservationSequence(t *testing.T) {
	manager, session := newDiscoveryManager(t)
	fullCapability := DiscoveryCapability{
		RemoteListeners: CapabilityFull,
		SocketIdentity:  CapabilityFull,
		ProcessMetadata: CapabilityFull,
	}
	session.facts <- ObservationSet{Sequence: 2, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
	waitForDiscoveryBaseline(t, manager, true)
	session.facts <- ObservationSet{Sequence: 2, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
	failed := waitForDiscoveryState(t, manager, DiscoveryFailed)
	if got := failed.Host.Discovery.Diagnostic; got != "invalid_session_fact" {
		t.Fatalf("stale sequence Diagnostic = %q, want invalid_session_fact", got)
	}
}

func TestManagerRejectsObservationBudgetViolations(t *testing.T) {
	for name, budget := range map[string]ObservationBudget{
		"missing":    {},
		"oversized":  {Listeners: maxRetainedListenerObservations + 1, Sockets: 256, ProcessRecords: 512, MetadataBytes: 128 << 10},
		"zeroSocket": {Listeners: 256, Sockets: 0, ProcessRecords: 512, MetadataBytes: 128 << 10},
	} {
		t.Run(name, func(t *testing.T) {
			manager, session := newDiscoveryManager(t)
			session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: budget}
			failed := waitForDiscoveryState(t, manager, DiscoveryFailed)
			if got := failed.Host.Discovery.Diagnostic; got != "invalid_session_fact" {
				t.Fatalf("budget %s Diagnostic = %q, want invalid_session_fact", name, got)
			}
		})
	}
}

func TestManagerAcceptsDeclaredObservationBudget(t *testing.T) {
	manager, session := newDiscoveryManager(t)
	session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget}
	waitForDiscoveryState(t, manager, DiscoveryHealthy)
}

func newDiscoveryManager(t *testing.T) (*manager, *scriptedDiscoverySession) {
	t.Helper()
	session := newScriptedDiscoverySession()
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: oneSessionConnector{session: session},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	manager.actor.start()
	waitForDiscoveryState(t, manager, DiscoveryStarting)
	return manager, session
}

func waitForDiscoveryBaseline(t *testing.T, manager Manager, established bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Host != nil && snapshot.Host.Discovery.BaselineEstablished == established {
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
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Host != nil && reflect.DeepEqual(snapshot.Host.Discovery.Capability, capability) {
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
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Host != nil && snapshot.Host.Discovery.State == state {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Discovery did not reach %q; last Snapshot: %#v", state, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}
