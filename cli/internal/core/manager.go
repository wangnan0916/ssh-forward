package core

import (
	"context"
	"sync"
	"time"
)

const (
	defaultRetryDelay      = time.Second
	maxTemporaryPortOffset = 20
	maximumTCPPort         = 1<<16 - 1
)

type managerOptions struct {
	host       HostAlias
	backend    Backend
	intent     ForwardingIntent
	retryDelay time.Duration
}

type manager struct {
	mu      sync.RWMutex
	closed  bool
	host    HostAlias
	backend Backend

	intent             ForwardingIntent
	discovery          DiscoveryStatus
	listeners          map[uint16]Listener
	states             map[forwardKey]ForwardStatus
	reservedLocalPorts map[uint16]struct{}
	forwardWorkers     map[forwardKey]*forwardWorker

	retryDelay time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	tasks      sync.WaitGroup
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
}

// NewManager observes host and reconciles remembered, automatic, and
// published forwards.
func NewManager(host HostAlias, backend Backend, intent ForwardingIntent) Manager {
	return newManager(managerOptions{host: host, backend: backend, intent: intent})
}

func newManager(options managerOptions) *manager {
	ctx, cancel := context.WithCancel(context.Background())
	if options.retryDelay <= 0 {
		options.retryDelay = defaultRetryDelay
	}
	intent := normalizedForwardingIntent(options.intent)
	m := &manager{
		host:               options.host,
		backend:            options.backend,
		intent:             intent,
		discovery:          DiscoveryStatus{State: DiscoveryConnecting},
		listeners:          make(map[uint16]Listener),
		states:             make(map[forwardKey]ForwardStatus),
		reservedLocalPorts: reservedLocalPorts(intent.PublishedForwards, intent.ReservedLocalPorts...),
		forwardWorkers:     make(map[forwardKey]*forwardWorker),
		retryDelay:         options.retryDelay,
		ctx:                ctx,
		cancel:             cancel,
		closeDone:          make(chan struct{}),
	}

	if m.backend == nil || m.host == "" {
		m.discovery = DiscoveryStatus{State: DiscoveryFailed, Diagnostic: "not_configured"}
		desiredForwards := buildDesiredForwards(m.intent.RememberedForwards, m.intent.PublishedForwards, m.listeners, m.intent.WorkingDirectoryRules, m.intent.AutoForwards...)
		for key, desired := range desiredForwards {
			m.states[key] = forwardStatus(desired, ForwardFailed, "not_configured", desired.preferred)
		}
		return m
	}
	m.mu.Lock()
	m.reconcileForwardsLocked()
	m.mu.Unlock()
	m.tasks.Go(m.observe)
	return m
}

// UpdateIntent reconciles new persistent intent without disturbing forwards
// that remain desired. It is safe to call repeatedly with equivalent intent.
func (m *manager) UpdateIntent(ctx context.Context, intent ForwardingIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	intent = normalizedForwardingIntent(intent)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.closed {
		return ErrManagerClosed
	}
	m.intent = intent
	m.reservedLocalPorts = reservedLocalPorts(intent.PublishedForwards, intent.ReservedLocalPorts...)
	m.reconcileForwardsLocked()
	return nil
}

func (m *manager) reconcileForwardsLocked() {
	desiredForwards := buildDesiredForwards(m.intent.RememberedForwards, m.intent.PublishedForwards, m.listeners, m.intent.WorkingDirectoryRules, m.intent.AutoForwards...)
	for key := range m.states {
		_, desired := desiredForwards[key]
		_, running := m.forwardWorkers[key]
		if !desired && !running {
			delete(m.states, key)
		}
	}
	workers := make(map[forwardKey]workerSnapshot, len(m.forwardWorkers))
	for key, worker := range m.forwardWorkers {
		workers[key] = workerSnapshot{desired: worker.desired, status: m.states[key]}
	}
	plan := planReconciliation(desiredForwards, workers, m.reservedLocalPorts)
	for _, desired := range plan.keep {
		key := desired.key()
		status := m.states[key]
		status.Automatic = desired.automatic
		m.states[key] = status
	}
	for _, key := range plan.stop {
		m.forwardWorkers[key].cancel()
	}
	for _, desired := range plan.wait {
		m.states[desired.key()] = forwardStatus(desired, ForwardStarting, "", desired.preferred)
	}
	for _, desired := range plan.start {
		m.startForwardLocked(desired)
	}
}

func (m *manager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()
		go func() {
			m.tasks.Wait()
			if m.backend != nil {
				m.closeErr = m.backend.Close(context.Background())
			}
			close(m.closeDone)
		}()
	})
	select {
	case <-m.closeDone:
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
