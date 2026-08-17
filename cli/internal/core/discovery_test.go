package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"ssh-forward/cli/internal/proxy"

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
	fullObservationBudget = ObservationBudget{Listeners: MaxRetainedListenerObservations, Sockets: MaxRetainedSocketIdentities, ProcessRecords: MaxRetainedProcessRecords, MetadataBytes: MaxRetainedProcessMetadataBytes}
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
		makeListeners(1, MaxRetainedListenerObservations),
		makeListeners(1+MaxRetainedListenerObservations, MaxRetainedListenerObservations),
	)
	if len(listeners) > MaxRetainedListenerObservations {
		t.Fatalf("retained Listener Observations = %d, want at most %d", len(listeners), MaxRetainedListenerObservations)
	}
	if !listenerTruncation.listeners {
		t.Fatal("Listener truncation was not reported")
	}
	retained := makeListeners(1000, MaxRetainedListenerObservations)
	withLowerCurrent, _ := mergeBoundedListenerObservations(retained, makeListeners(1, 1))
	if diff := cmp.Diff(withLowerCurrent, retained); diff != "" {
		t.Fatalf("partial merge evicted retained Listener Observation for a newly observed lower-sorting key (-got +want):\n%s", diff)
	}

	makeEvidence := func(first int) ListenerObservation {
		observation := ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 8080}
		for index := range MaxRetainedSocketIdentities {
			identity := first + index
			observation.SocketIdentities = append(observation.SocketIdentities, SocketIdentity(fmt.Sprintf("socket:%04d", identity)))
			observation.Processes = append(observation.Processes, ProcessChain{Processes: []ProcessMetadata{{PID: identity + 1}}})
		}
		return observation
	}
	evidence, truncatedEvidence := mergeBoundedListenerObservations(
		[]ListenerObservation{makeEvidence(0)},
		[]ListenerObservation{makeEvidence(MaxRetainedSocketIdentities)},
	)
	if got := len(evidence[0].SocketIdentities); got > MaxRetainedSocketIdentities {
		t.Fatalf("retained Socket Identities = %d, want at most %d", got, MaxRetainedSocketIdentities)
	}
	if got := len(evidence[0].Processes); got > MaxRetainedProcessRecords {
		t.Fatalf("retained Process Chains = %d, want at most %d", got, MaxRetainedProcessRecords)
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

func TestManagerReportsEvidenceTruncationDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		ready := make(chan struct{})
		close(ready)
		connector := &sequenceConnector{
			sessions: []hostSession{session},
			releases: []<-chan struct{}{ready},
			started:  make(chan int, 1),
		}
		owner := &scriptedOwnedForward{closeStart: make(chan struct{}), closeDone: make(chan struct{})}
		manager, closeManager := newBubbleForwardingManager(t, connector, owner)
		defer closeManager()

		observations := make([]ListenerObservation, MaxRetainedListenerObservations+1)
		for index := range observations {
			observations[index] = ListenerObservation{
				Family:     FamilyIPv4,
				BindScope:  BindLoopback,
				RemotePort: uint16(1000 + index),
			}
		}
		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Observations: observations, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if got := snapshot.Host.Discovery.Diagnostic; got != "evidence_truncated" {
			t.Fatalf("truncated discovery diagnostic = %q, want evidence_truncated", got)
		}
		if got := snapshot.Host.Discovery.State; got != DiscoveryDegraded {
			t.Fatalf("truncated discovery state = %q, want degraded", got)
		}
	})
}

func TestManagerRejectsUnknownCapabilityReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		ready := make(chan struct{})
		close(ready)
		connector := &sequenceConnector{
			sessions: []hostSession{session},
			releases: []<-chan struct{}{ready},
			started:  make(chan int, 1),
		}
		owner := &scriptedOwnedForward{closeStart: make(chan struct{}), closeDone: make(chan struct{})}
		manager, closeManager := newBubbleForwardingManager(t, connector, owner)
		defer closeManager()

		session.facts <- ObservationSet{
			Sequence:         1,
			Capability:       fullTestCapability,
			Budget:           fullObservationBudget,
			CapabilityReason: CapabilityReason("made_up_reason"),
		}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if got := snapshot.Host.Discovery.State; got != DiscoveryFailed {
			t.Fatalf("Discovery state = %q, want %q", got, DiscoveryFailed)
		}
		if got := snapshot.Host.Discovery.Diagnostic; got != "invalid_session_fact" {
			t.Fatalf("unknown capability reason diagnostic = %q, want invalid_session_fact", got)
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
		manager, closeManager := newBubbleForwardingManager(t, connector, owner)
		defer closeManager()

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
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if !snapshot.Host.Discovery.BaselineEstablished {
			t.Fatalf("first observation did not establish baseline: %#v", snapshot.Host.Discovery)
		}
		first.terminal <- &SessionError{Disposition: SessionRetry, Reason: SessionReasonTransport}
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after reconnect: %v", err)
		}
		if got := snapshot.Host.Discovery.State; got != DiscoveryStarting {
			t.Fatalf("Discovery state after reconnect = %q, want %q", got, DiscoveryStarting)
		}
		if !cmp.Equal(snapshot.Host.ListenerObservations, []ListenerObservation{observation}) {
			t.Fatalf("reconnect discarded retained observations (-got +want):\n%s", cmp.Diff(snapshot.Host.ListenerObservations, []ListenerObservation{observation}))
		}
		partial := DiscoveryCapability{
			RemoteListeners: CapabilityPartial,
			SocketIdentity:  CapabilityPartial,
			ProcessMetadata: CapabilityUnavailable,
		}
		second.facts <- ObservationSet{Sequence: 1, Capability: partial, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after partial reconnect: %v", err)
		}
		if snapshot.Host.Discovery.BaselineEstablished {
			t.Fatalf("partial reconnect established baseline: %#v", snapshot.Host.Discovery)
		}
		if !cmp.Equal(snapshot.Host.ListenerObservations, []ListenerObservation{observation}) {
			t.Fatalf("partial reconnect replaced retained observations (-got +want):\n%s", cmp.Diff(snapshot.Host.ListenerObservations, []ListenerObservation{observation}))
		}
	})
}

func TestManagerPublishesDiscoveryBaselineAtomically(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			owner.release()
			_ = manager.Close(ctx)
		}()
		if _, err := manager.Execute(t.Context(), AddManualForward{
			CommandID:  CommandID("operation-add"),
			Host:       HostAlias("development"),
			RemotePort: 8080,
			Family:     FamilyAuto,
		}); err != nil {
			t.Fatalf("add Manual Forward: %v", err)
		}
		synctest.Wait()
		starting, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		stream, err := manager.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		initial, err := stream.Next(t.Context())
		if err != nil {
			t.Fatalf("initial Next: %v", err)
		}
		if diff := cmp.Diff(initial, starting); diff != "" {
			t.Fatalf("Watch initial Snapshot mismatch (-got +want):\n%s", diff)
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
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		baseline, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("baseline Next: %v", err)
		}
		if baseline.Revision != starting.Revision+1 {
			t.Fatalf("baseline revision = %d, want %d", baseline.Revision, starting.Revision+1)
		}
		if got := baseline.Host.Discovery; got.State != DiscoveryDegraded || !got.BaselineEstablished || !cmp.Equal(got.Capability, capability) {
			t.Fatalf("Discovery = %#v, want atomic degraded baseline with %#v", got, capability)
		}
		if got := baseline.Host.ListenerObservations; !cmp.Equal(got, []ListenerObservation{observation}) {
			t.Fatalf("Listener Observations mismatch (-got +want):\n%s", cmp.Diff(got, []ListenerObservation{observation}))
		}
		baseline.Host.ListenerObservations[0].Processes[0].Processes[0].Arguments[0] = "mutated"
		immutable, err := manager.Snapshot(t.Context())
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
		if got := partial.Host.Discovery; got.State != DiscoveryDegraded || !got.BaselineEstablished || !cmp.Equal(got.Capability, partialCapability) {
			t.Fatalf("partial Discovery = %#v, want degraded retained baseline with %#v", got, partialCapability)
		}
		merged := partial.Host.ListenerObservations
		if len(merged) != 1 || !cmp.Equal(merged[0].SocketIdentities, []SocketIdentity{SocketIdentity("socket:new"), SocketIdentity("socket:test")}) || len(merged[0].Processes) != 2 {
			t.Fatalf("partial observation did not merge retained and current evidence: %#v", merged)
		}

		boundedObservation := ListenerObservation{Family: FamilyIPv4, BindScope: BindLoopback, RemotePort: 38080}
		for index := range MaxRetainedSocketIdentities {
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
		if len(boundedEvidence.SocketIdentities) > MaxRetainedSocketIdentities || len(boundedEvidence.Processes) > MaxRetainedProcessRecords {
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
	})
}

func TestManagerDegradesDiscoveryOnObservationSequenceGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session := newScriptedDiscoverySession()
		manager := newManager(managerOptions{
			host:      HostAlias("development"),
			connector: oneSessionConnector{session: session},
		})
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}()
		manager.actor.startIfNeeded()
		// Wait replaces waitForSnapshot polling: the bubble's virtual
		// scheduler runs until every goroutine is durably blocked, so by
		// the time Wait returns the actor has published its current state.
		synctest.Wait()
		snapshotOf := func() Snapshot {
			snapshot, err := manager.Snapshot(t.Context())
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			return snapshot
		}
		if got := snapshotOf().Host.Discovery.State; got != DiscoveryStarting {
			t.Fatalf("Discovery state = %q, want %q", got, DiscoveryStarting)
		}

		fullCapability := DiscoveryCapability{
			RemoteListeners: CapabilityFull,
			SocketIdentity:  CapabilityFull,
			ProcessMetadata: CapabilityFull,
		}
		session.facts <- ObservationSet{Sequence: 1, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
		synctest.Wait()
		if baseline := snapshotOf(); baseline.Host.Discovery.Diagnostic != "" {
			t.Fatalf("baseline Diagnostic = %q, want empty", baseline.Host.Discovery.Diagnostic)
		}

		session.facts <- ObservationSet{Sequence: 3, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
		synctest.Wait()
		gapped := snapshotOf()
		if got := gapped.Host.Discovery.Diagnostic; got != "observation_resync" {
			t.Fatalf("gap Diagnostic = %q, want observation_resync", got)
		}
		if !gapped.Host.Discovery.BaselineEstablished {
			t.Fatal("sequence gap discarded the established Baseline")
		}
		if got := gapped.Host.Discovery.State; got != DiscoveryDegraded {
			t.Fatalf("gap state = %q, want %q", got, DiscoveryDegraded)
		}

		session.facts <- ObservationSet{Sequence: 4, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
		synctest.Wait()
		recovered := snapshotOf()
		if got := recovered.Host.Discovery.Diagnostic; got != "" {
			t.Fatalf("recovered Diagnostic = %q, want empty", got)
		}
		if got := recovered.Host.Discovery.State; got != DiscoveryHealthy {
			t.Fatalf("recovered state = %q, want %q", got, DiscoveryHealthy)
		}
	})
}

func TestManagerRejectsStaleObservationSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()
		fullCapability := DiscoveryCapability{
			RemoteListeners: CapabilityFull,
			SocketIdentity:  CapabilityFull,
			ProcessMetadata: CapabilityFull,
		}
		session.facts <- ObservationSet{Sequence: 2, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if !snapshot.Host.Discovery.BaselineEstablished {
			t.Fatalf("first observation did not establish baseline: %#v", snapshot.Host.Discovery)
		}
		session.facts <- ObservationSet{Sequence: 2, Capability: fullCapability, Observations: []ListenerObservation{}, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err = manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot after stale sequence: %v", err)
		}
		if got := snapshot.Host.Discovery.State; got != DiscoveryFailed {
			t.Fatalf("stale sequence state = %q, want %q", got, DiscoveryFailed)
		}
		if got := snapshot.Host.Discovery.Diagnostic; got != "invalid_session_fact" {
			t.Fatalf("stale sequence Diagnostic = %q, want invalid_session_fact", got)
		}
	})
}

func TestManagerRejectsObservationBudgetViolations(t *testing.T) {
	for name, budget := range map[string]ObservationBudget{
		"missing":    {},
		"oversized":  {Listeners: MaxRetainedListenerObservations + 1, Sockets: 256, ProcessRecords: 512, MetadataBytes: 128 << 10},
		"zeroSocket": {Listeners: 256, Sockets: 0, ProcessRecords: 512, MetadataBytes: 128 << 10},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manager, session, closeManager := newBubbleDiscoveryManager(t)
				defer closeManager()
				session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: budget}
				synctest.Wait()
				snapshot, err := manager.Snapshot(t.Context())
				if err != nil {
					t.Fatalf("Snapshot: %v", err)
				}
				if got := snapshot.Host.Discovery.State; got != DiscoveryFailed {
					t.Fatalf("budget %s state = %q, want %q", name, got, DiscoveryFailed)
				}
				if got := snapshot.Host.Discovery.Diagnostic; got != "invalid_session_fact" {
					t.Fatalf("budget %s Diagnostic = %q, want invalid_session_fact", name, got)
				}
			})
		})
	}
}

func TestManagerAcceptsDeclaredObservationBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, session, closeManager := newBubbleDiscoveryManager(t)
		defer closeManager()
		session.facts <- ObservationSet{Sequence: 1, Capability: fullTestCapability, Budget: fullObservationBudget}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if got := snapshot.Host.Discovery.State; got != DiscoveryHealthy {
			t.Fatalf("Discovery state = %q, want %q", got, DiscoveryHealthy)
		}
	})
}

// newBubbleDiscoveryManager builds a scripted Manager inside a synctest
// bubble and settles until the actor publishes DiscoveryStarting. The
// returned closeManager must run before the bubble test function returns:
// the Manager's goroutines belong to the bubble, so shutdown must happen
// inside it.
func newBubbleDiscoveryManager(t *testing.T) (*manager, *scriptedDiscoverySession, func()) {
	t.Helper()
	session := newScriptedDiscoverySession()
	manager := newManager(managerOptions{
		host:      HostAlias("development"),
		connector: oneSessionConnector{session: session},
	})
	manager.actor.startIfNeeded()
	synctest.Wait()
	snapshot, err := manager.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Host.Discovery.State; got != DiscoveryStarting {
		t.Fatalf("Discovery state = %q, want %q", got, DiscoveryStarting)
	}
	closeManager := func() {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	}
	return manager, session, closeManager
}

// newBubbleForwardingManager builds a scripted Manager with a Manual Forward
// already added inside a synctest bubble, settling until the actor publishes
// DiscoveryStarting. The returned closeManager must run before the bubble
// test function returns.
func newBubbleForwardingManager(t *testing.T, connector *sequenceConnector, owner *scriptedOwnedForward) (*manager, func()) {
	t.Helper()
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
	if _, err := manager.Execute(t.Context(), AddManualForward{
		CommandID:  CommandID("operation-add"),
		Host:       HostAlias("development"),
		RemotePort: 8080,
		Family:     FamilyAuto,
	}); err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	synctest.Wait()
	snapshot, err := manager.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Host.Discovery.State; got != DiscoveryStarting {
		t.Fatalf("Discovery state = %q, want %q", got, DiscoveryStarting)
	}
	closeManager := func() {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		owner.release()
		_ = manager.Close(ctx)
	}
	return manager, closeManager
}
