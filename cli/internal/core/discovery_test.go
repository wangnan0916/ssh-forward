package core

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
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

func (*scriptedDiscoverySession) DialContext(context.Context, netip.AddrPort) (HalfCloseConn, error) {
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
	session HostSession
}

func (c oneSessionConnector) Connect(context.Context, HostAlias) (HostSession, error) {
	return c.session, nil
}

var fullTestCapability = DiscoveryCapability{
	RemoteListeners: CapabilityFull,
	ProcessMetadata: CapabilityFull,
}

func TestObservationBounds(t *testing.T) {
	listeners := make([]ListenerObservation, MaxRetainedListenerObservations+1)
	for index := range listeners {
		listeners[index] = ListenerObservation{
			Family:     FamilyIPv4,
			BindScope:  BindLoopback,
			RemotePort: uint16(1000 + index),
		}
	}
	bounded, truncated := boundListenerObservations(listeners)
	if len(bounded) != MaxRetainedListenerObservations || !truncated.listeners {
		t.Fatalf("listener bounds = %d, %#v", len(bounded), truncated)
	}

	processes := ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080}
	for index := range MaxRetainedProcessRecords + 1 {
		processes.Processes = append(processes.Processes, ProcessChain{Processes: []ProcessMetadata{{PID: index + 1}}})
	}
	bounded, truncated = boundListenerObservations([]ListenerObservation{processes})
	if got := len(bounded[0].Processes); got != MaxRetainedProcessRecords || !truncated.processes {
		t.Fatalf("process bounds = %d, %#v", got, truncated)
	}
}

func TestManagerReportsEvidenceTruncation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()
		observations := make([]ListenerObservation, MaxRetainedListenerObservations+1)
		for index := range observations {
			observations[index] = ListenerObservation{
				Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: uint16(1000 + index),
			}
		}
		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: observations}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if got := snapshot.Host.Discovery; got.State != DiscoveryDegraded || got.Diagnostic != "scanner_reported_partial" {
			t.Fatalf("truncated Discovery = %#v", got)
		}
	})
}

func TestManagerRetainsObservationsUntilReconnectGetsCompleteReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := newScriptedDiscoverySession()
		second := newScriptedDiscoverySession()
		ready := make(chan struct{})
		close(ready)
		connector := &sequenceConnector{
			sessions: []HostSession{first, second},
			releases: []<-chan struct{}{ready, ready},
			started:  make(chan int, 2),
		}
		manager, closeManager := newBubbleForwardingManager(t, connector)
		defer closeManager()

		observation := ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 38080, Processes: []ProcessChain{}}
		first.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: []ListenerObservation{observation}}
		synctest.Wait()
		first.terminal <- &SessionError{Disposition: SessionRetry, Reason: SessionReasonTransport}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after reconnect: %v", err)
		}
		if snapshot.Host.Discovery.State != DiscoveryStarting || !cmp.Equal(snapshot.Host.ListenerObservations, []ListenerObservation{observation}) {
			t.Fatalf("reconnect discarded retained observation: %#v", snapshot.Host)
		}

		partial := DiscoveryCapability{RemoteListeners: CapabilityPartial, ProcessMetadata: CapabilityUnavailable}
		second.facts <- ObservationSet{Sequence: 1, Capability: partial}
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after partial reconnect: %v", err)
		}
		if snapshot.Host.Discovery.BaselineEstablished || !cmp.Equal(snapshot.Host.ListenerObservations, []ListenerObservation{observation}) {
			t.Fatalf("partial reconnect replaced retained observation: %#v", snapshot.Host)
		}
	})
}

func TestManagerPublishesAndMergesDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		manager := newManager(managerOptions{host: HostAlias("development"), connector: oneSessionConnector{session: session}})
		defer closeTestManager(t, manager)
		synctest.Wait()
		starting, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		stream, err := manager.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if initial, err := stream.Next(t.Context()); err != nil || !cmp.Equal(initial, starting) {
			t.Fatalf("initial Watch = %#v, %v", initial, err)
		}

		capability := DiscoveryCapability{RemoteListeners: CapabilityFull, ProcessMetadata: CapabilityPartial}
		observation := ListenerObservation{
			Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 38080,
			Processes: []ProcessChain{{Processes: []ProcessMetadata{{
				PID: 42, Executable: "/usr/bin/python3", WorkingDirectory: "/workspace", Arguments: []string{"python3", "fixture.py"},
			}}}},
		}
		session.facts <- ObservationSet{Sequence: 1, Capability: capability, Observations: []ListenerObservation{observation}}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		baseline, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("baseline Next: %v", err)
		}
		if baseline.Revision != starting.Revision+1 || !baseline.Host.Discovery.BaselineEstablished || !cmp.Equal(baseline.Host.ListenerObservations, []ListenerObservation{observation}) {
			t.Fatalf("baseline = %#v", baseline)
		}
		baseline.Host.ListenerObservations[0].Processes[0].Processes[0].Arguments[0] = "mutated"
		immutable, _ := manager.Snapshot(t.Context())
		if got := immutable.Host.ListenerObservations[0].Processes[0].Processes[0].Arguments[0]; got != "python3" {
			t.Fatalf("caller mutation changed canonical metadata: %q", got)
		}

		partial := ListenerObservation{
			Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 38080,
			Processes: []ProcessChain{{Processes: []ProcessMetadata{{PID: 43, Executable: "/usr/bin/new-owner"}}}},
		}
		session.facts <- ObservationSet{
			Sequence:     2,
			Capability:   DiscoveryCapability{RemoteListeners: CapabilityPartial, ProcessMetadata: CapabilityPartial},
			Observations: []ListenerObservation{partial},
		}
		merged, err := stream.Next(ctx)
		if err != nil || len(merged.Host.ListenerObservations[0].Processes) != 2 {
			t.Fatalf("partial merge = %#v, %v", merged, err)
		}

		session.facts <- ObservationSet{Sequence: 2, Capability: capability}
		failed, err := stream.Next(ctx)
		if err != nil || failed.Host.Discovery.State != DiscoveryFailed || failed.Host.Discovery.Diagnostic != "invalid_session_fact" {
			t.Fatalf("invalid fact = %#v, %v", failed, err)
		}
	})
}

func TestManagerDegradesDiscoveryOnObservationSequenceGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()
		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability}
		synctest.Wait()
		session.facts <- ObservationSet{Sequence: 3, Capability: fullTestCapability}
		synctest.Wait()
		gapped, _ := manager.Snapshot(t.Context())
		if got := gapped.Host.Discovery; got.State != DiscoveryDegraded || got.Diagnostic != "observation_resync" || !got.BaselineEstablished {
			t.Fatalf("gapped Discovery = %#v", got)
		}
		session.facts <- ObservationSet{Sequence: 4, Capability: fullTestCapability}
		synctest.Wait()
		recovered, _ := manager.Snapshot(t.Context())
		if got := recovered.Host.Discovery; got.State != DiscoveryHealthy || got.Diagnostic != "" {
			t.Fatalf("recovered Discovery = %#v", got)
		}
	})
}

func TestManagerRejectsStaleObservationSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()
		session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability}
		synctest.Wait()
		session.facts <- ObservationSet{Sequence: 2, Capability: fullTestCapability}
		synctest.Wait()
		snapshot, _ := manager.Snapshot(t.Context())
		if got := snapshot.Host.Discovery; got.State != DiscoveryFailed || got.Diagnostic != "invalid_session_fact" {
			t.Fatalf("stale sequence Discovery = %#v", got)
		}
	})
}

func newBubbleDiscoveryManager(t *testing.T) (*manager, *scriptedDiscoverySession, func()) {
	t.Helper()
	session := newScriptedDiscoverySession()
	manager := newManager(managerOptions{host: HostAlias("development"), connector: oneSessionConnector{session: session}})
	manager.actor.startIfNeeded()
	synctest.Wait()
	return manager, session, func() { closeTestManager(t, manager) }
}

func newBubbleForwardingManager(t *testing.T, connector *sequenceConnector) (*manager, func()) {
	t.Helper()
	manager := newManager(managerOptions{
		host: HostAlias("development"), connector: connector,
		retryDelay: func(int) time.Duration { return 0 },
		retryWait:  func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
	})
	synctest.Wait()
	return manager, func() { closeTestManager(t, manager) }
}

func closeTestManager(t *testing.T, manager *manager) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = manager.Close(ctx)
}

func TestAdmitObservationSet(t *testing.T) {
	valid := ObservationSet{Sequence: 2, Capability: fullTestCapability}
	cases := []struct {
		name   string
		set    ObservationSet
		last   uint64
		gapped bool
		ok     bool
	}{
		{name: "next", set: valid, last: 1, ok: true},
		{name: "gap", set: ObservationSet{Sequence: 4, Capability: fullTestCapability}, last: 1, gapped: true, ok: true},
		{name: "zero", set: ObservationSet{Capability: fullTestCapability}},
		{name: "stale", set: valid, last: 2},
		{name: "bad capability", set: ObservationSet{Sequence: 1, Capability: DiscoveryCapability{RemoteListeners: "nope"}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			gapped, ok := admitObservationSet(test.set, test.last)
			if gapped != test.gapped || ok != test.ok {
				t.Fatalf("admitObservationSet = %v, %v", gapped, ok)
			}
		})
	}
}

func TestAdmitDiscoveryChange(t *testing.T) {
	valid := DiscoveryChange{State: DiscoveryDegraded, Capability: fullTestCapability, Reason: ReasonObservationLost}
	if !admitDiscoveryChange(valid) {
		t.Fatal("rejected valid DiscoveryChange")
	}
	if admitDiscoveryChange(DiscoveryChange{State: DiscoveryHealthy, Capability: fullTestCapability, Reason: ReasonObservationLost}) ||
		admitDiscoveryChange(DiscoveryChange{State: DiscoveryDegraded, Reason: ReasonObservationLost}) {
		t.Fatal("accepted invalid DiscoveryChange")
	}
}

func TestDiscoveryDiagnostic(t *testing.T) {
	partialListeners := DiscoveryCapability{RemoteListeners: CapabilityPartial, ProcessMetadata: CapabilityFull}
	partialProcess := DiscoveryCapability{RemoteListeners: CapabilityFull, ProcessMetadata: CapabilityPartial}
	cases := []struct {
		name       string
		gapped     bool
		capability DiscoveryCapability
		failure    DiscoveryReason
		want       string
	}{
		{name: "invalid observation", failure: ReasonObservationInvalid, want: "invalid_scanner_frame"},
		{name: "lost observation", failure: ReasonObservationLost, want: "scanner_framing_failed"},
		{name: "invalid session", failure: ReasonSessionInvalid, want: "invalid_session_fact"},
		{name: "gap", gapped: true, capability: fullTestCapability, want: "observation_resync"},
		{name: "partial listeners", capability: partialListeners, want: "scanner_reported_partial"},
		{name: "partial process", capability: partialProcess, want: "process_metadata_unavailable"},
		{name: "full", capability: fullTestCapability},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := discoveryDiagnostic(test.gapped, test.capability, test.failure); got != test.want {
				t.Fatalf("discoveryDiagnostic = %q, want %q", got, test.want)
			}
		})
	}
}
