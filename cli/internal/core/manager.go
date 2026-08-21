package core

import (
	"context"
	"errors"
	"sync"
	"time"
)

type managerOptions struct {
	host             HostAlias
	connector        HostConnector
	forwardAllocator ForwardAllocator
	newAllocator     func(Dialer) ForwardAllocator
	retryDelay       func(int) time.Duration
	retryWait        func(context.Context, time.Duration) bool
	policies         func() ([]ForwardingPolicy, string)
	policyPoll       time.Duration
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

func NewManager() Manager {
	return newManager(managerOptions{})
}

const defaultPolicyPoll = 250 * time.Millisecond

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
	go m.reconcileLoop(options.policyPoll)
	m.actor = newHostActor(hostActorOptions{
		host:          options.host,
		connector:     options.connector,
		dialer:        dialer,
		publish:       m.publishHostState,
		onObservation: m.reconciler.notify,
		ctx:           ctx,
		retryDelay:    options.retryDelay,
		retryWait:     options.retryWait,
	})
	m.snapshot = m.composeSnapshotLocked()
	if options.connector != nil && options.host != "" {
		m.actor.startIfNeeded()
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
