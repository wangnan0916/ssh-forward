package core

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// managerOptions is the Manager's single configuration surface — and a test
// seam: every field except host has an injected default, and the core tests
// replace them to script time, concurrency, and failure. Do not wire these
// in production code paths outside newManager. publishHost is the strongest
// knob: when it blocks, the Manager state lock blocks, and any goroutine
// waiting on it (including the actor) stalls — tests use it as a deliberate
// freeze window, production must never.
type managerOptions struct {
	host             HostAlias
	connector        hostConnector
	forwardAllocator forwardAllocator
	publishHost      func(HostSnapshot)
	retryDelay       func(int) time.Duration
	retryWait        func(context.Context, time.Duration) bool
	// policies is the Forwarding Policy source seam: the reconciliation
	// path refreshes its policy cache from this function outside the
	// Manager lock. nil means no policies (unmatched listeners are not
	// forwarded).
	policies func() []ForwardingPolicy
}

type manager struct {
	mu               sync.RWMutex
	closed           bool
	host             HostAlias
	forwardAllocator forwardAllocator
	revision         Revision
	hostSnapshot     HostSnapshot
	actor            *hostActor
	forwards         forwardTable
	snapshot         Snapshot
	watchers         map[uint64]*snapshotStream
	nextWatchID      uint64

	reconciler *reconciler

	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

// NewManager builds a Manager with no host and no connector: it exists for
// wire-level tests (jsonrpc fixtures) and core tests that exercise Snapshot
// and Watch without a host. Production assembly goes through
// NewConfiguredManager via app.NewManager.
func NewManager() Manager {
	return newManager(managerOptions{})
}

// NewConfiguredManager is the production seam: it wires the host, the host
// connector (the OpenSSH Adapter), and the Forwarding Policy source (slice
// 5; nil means unmatched listeners are not forwarded) into the Manager. app.NewManager is its only
// production caller; tests inject through managerOptions instead.
func NewConfiguredManager(host HostAlias, connector HostConnector, policySources ...func() []ForwardingPolicy) Manager {
	var policies func() []ForwardingPolicy
	if len(policySources) != 0 {
		policies = policySources[0]
	}
	return newManager(managerOptions{host: host, connector: connector, policies: policies})
}

func newManager(options managerOptions) *manager {
	ctx, cancel := context.WithCancel(context.Background())
	retryDelay := options.retryDelay
	if retryDelay == nil {
		retryDelay = exponentialJitterDelay
	}
	retryWait := options.retryWait
	if retryWait == nil {
		retryWait = waitForRetry
	}
	dialer := &currentDialer{}
	allocator := options.forwardAllocator
	if allocator == nil {
		allocator = proxyForwardAllocator{dialer: dialer}
	}
	policySource := options.policies
	if policySource == nil {
		policySource = func() []ForwardingPolicy { return nil }
	}
	m := &manager{
		host:             options.host,
		forwardAllocator: allocator,
		forwards:         newForwardTable(),
		watchers:         make(map[uint64]*snapshotStream),
		reconciler:       newReconciler(policySource),
		hostSnapshot:     emptyHostSnapshot(options.host),
		ctx:              ctx,
		cancel:           cancel,
	}
	m.workers.Add(1)
	go m.reconcileLoop()
	publishHost := m.publishHostState
	if options.publishHost != nil {
		publishHost = options.publishHost
	}
	m.actor = newHostActor(hostActorOptions{
		host:          options.host,
		connector:     options.connector,
		dialer:        dialer,
		publish:       publishHost,
		onObservation: m.reconciler.notify,
		ctx:           ctx,
	}, retryDelay, retryWait)
	m.snapshot = m.buildSnapshotLocked()
	if options.connector != nil && options.host != "" {
		m.ensureConnected()
	}
	return m
}

// emptyHostSnapshot is the single construction of the pre-connection state
// shape: the Manager seeds its mirror with it, and the hostActor's internal
// state starts from it, so the two cannot drift in initial shape.
func emptyHostSnapshot(host HostAlias) HostSnapshot {
	return HostSnapshot{
		Alias:                host,
		Connection:           ConnectionDisconnected,
		Discovery:            stoppedDiscovery(),
		ListenerObservations: make([]ListenerObservation, 0),
	}
}

// publishHostState is the actor's publication path into the Manager's mirror:
// it replaces the mirror wholesale and publishes. ensureConnected is the
// startup path's declaration — it patches the mirror to Connecting under the
// Manager lock and publishes once, so the transition is visible before the
// actor's connect loop runs. Both write under the Manager lock and both treat
// HostSnapshot as the single state structure; the actor takes over state
// evolution from Connected on.
// beginConnectionLocked is the Connecting declaration: it patches the
// mirror under the Manager lock. This is the one place the Manager writes
// the mirror directly — structurally forced by lock order (the actor lock
// can never be taken inside the Manager lock) and no-wait arming. The guard
// reads the actor's own armed() projection instead of the mirror state.
func (m *manager) beginConnectionLocked() {
	if m.actor.armed() {
		return
	}
	declared := m.hostSnapshot
	declared.Connection = ConnectionConnecting
	m.hostSnapshot = declared
}

func (m *manager) publishHostState(state HostSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.hostSnapshot = state
	m.publishLocked()
}

// ensureConnected declares Connecting and arms the actor. Production
// managers do this at construction so discovery and Auto-forward run
// without a separate command.
func (m *manager) ensureConnected() {
	m.mu.Lock()
	m.beginConnectionLocked()
	m.publishLocked()
	m.mu.Unlock()
	m.actor.startIfNeeded()
}

func loopbackTarget(family AddressFamily, port uint16) netip.AddrPort {
	if family == FamilyIPv6 {
		return netip.AddrPortFrom(netip.IPv6Loopback(), port)
	}
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port)
}

func familyForAddress(address netip.Addr) AddressFamily {
	if address.Is4() {
		return FamilyIPv4
	}
	return FamilyIPv6
}

func (m *manager) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSnapshot(m.snapshot), nil
}

func cloneForward(forward ForwardSnapshot) ForwardSnapshot {
	forward.LocalFamilies = append([]AddressFamily(nil), forward.LocalFamilies...)
	return forward
}

func (m *manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		for id, stream := range m.watchers {
			m.closeSnapshotStreamLocked(id, stream, &DomainError{Kind: ErrorManagerClosed, Retryable: true})
		}
	}
	forwards := m.forwards.owners()
	m.mu.Unlock()

	var errs []error
	for _, forward := range forwards {
		errs = append(errs, forward.Close(ctx))
	}
	errs = append(errs, m.actor.closeSession(ctx))
	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		if m.actor.isActive() {
			<-m.actor.done
		}
		close(done)
	}()
	select {
	case <-done:
		return errors.Join(errs...)
	case <-ctx.Done():
		return errors.Join(append(errs, ctx.Err())...)
	}
}
