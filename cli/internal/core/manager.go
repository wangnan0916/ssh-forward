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
	connector        HostConnector
	forwardAllocator ForwardAllocator
	newAllocator     func(Dialer) ForwardAllocator
	publishHost      func(hostView)
	retryDelay       func(int) time.Duration
	retryWait        func(context.Context, time.Duration) bool
	// policies is the Forwarding Policy source seam: the reconciliation
	// path refreshes its policy cache from this function outside the
	// Manager lock. The string is Policy Diagnostic when last-valid
	// policies are in effect because the file could not be parsed. nil
	// means no policies (unmatched listeners are not forwarded).
	policies func() ([]ForwardingPolicy, string)
	// policyPoll, when > 0, re-reads the policy source on this interval so
	// a saved Remembered Auto-forward applies against the current
	// observations without waiting for the next scan.
	policyPoll time.Duration
}

type manager struct {
	mu               sync.RWMutex
	closed           bool
	host             HostAlias
	forwardAllocator ForwardAllocator
	revision         Revision
	view             hostView
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
// NewConfiguredManager.
func NewManager() Manager {
	return newManager(managerOptions{})
}

const defaultPolicyPoll = 250 * time.Millisecond

// NewConfiguredManager is the production seam: it wires the host, the host
// connector, the Local Endpoint allocator factory, and the Forwarding Policy
// source into the Manager. app.Connect / app.Serve and integration tests are
// its callers; core tests inject through managerOptions instead.
func NewConfiguredManager(host HostAlias, connector HostConnector, newAlloc func(Dialer) ForwardAllocator, policySources ...func() ([]ForwardingPolicy, string)) Manager {
	var policies func() ([]ForwardingPolicy, string)
	if len(policySources) != 0 {
		policies = policySources[0]
	}
	return newManager(managerOptions{
		host:         host,
		connector:    connector,
		newAllocator: newAlloc,
		policies:     policies,
		policyPoll:   defaultPolicyPoll,
	})
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
	if allocator == nil && options.newAllocator != nil {
		allocator = options.newAllocator(dialer)
	}
	if allocator == nil {
		allocator = refusingAllocator{}
	}
	policySource := options.policies
	if policySource == nil {
		policySource = func() ([]ForwardingPolicy, string) { return nil, "" }
	}
	m := &manager{
		host:             options.host,
		forwardAllocator: allocator,
		forwards:         newForwardTable(),
		watchers:         make(map[uint64]*snapshotStream),
		reconciler:       newReconciler(policySource),
		view:             emptyHostView(options.host),
		ctx:              ctx,
		cancel:           cancel,
	}
	m.workers.Add(1)
	go m.reconcileLoop()
	if options.policyPoll > 0 {
		m.workers.Add(1)
		go m.policyPollLoop(options.policyPoll)
	}
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
	m.snapshot = m.composeSnapshotLocked()
	if options.connector != nil && options.host != "" {
		m.ensureConnected()
	}
	return m
}

func (m *manager) publishHostState(view hostView) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.view = view
	m.publishLocked()
}

// ensureConnected arms the host actor. The actor publishes Connecting
// before returning so the transition is visible without a separate write
// on the Manager mirror.
func (m *manager) ensureConnected() {
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
