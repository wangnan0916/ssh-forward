package core

import (
	"context"
	"slices"
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

type forwardWorker struct {
	ctx     context.Context
	cancel  context.CancelFunc
	desired desiredForward
}

type manager struct {
	mu      sync.RWMutex
	closed  bool
	host    HostAlias
	backend Backend

	discovery             DiscoveryStatus
	listeners             map[uint16]Listener
	states                map[forwardKey]ForwardStatus
	remembered            []RememberedForward
	published             []PublishedForward
	reservedLocalPorts    map[uint16]struct{}
	workingDirectoryRules []string
	forwardWorkers        map[forwardKey]*forwardWorker

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
		host:                  options.host,
		backend:               options.backend,
		discovery:             DiscoveryStatus{State: DiscoveryConnecting},
		listeners:             make(map[uint16]Listener),
		states:                make(map[forwardKey]ForwardStatus),
		remembered:            intent.RememberedForwards,
		published:             intent.PublishedForwards,
		reservedLocalPorts:    reservedLocalPorts(intent.PublishedForwards),
		workingDirectoryRules: intent.WorkingDirectoryRules,
		forwardWorkers:        make(map[forwardKey]*forwardWorker),
		retryDelay:            options.retryDelay,
		ctx:                   ctx,
		cancel:                cancel,
		closeDone:             make(chan struct{}),
	}

	if m.backend == nil || m.host == "" {
		m.discovery = DiscoveryStatus{State: DiscoveryFailed, Diagnostic: "not_configured"}
		desiredForwards := buildDesiredForwards(
			m.remembered, m.published, m.listeners, m.workingDirectoryRules,
		)
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

func (m *manager) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return Status{}, ErrManagerClosed
	}
	activePublishedPorts := make(map[uint16]struct{})
	for key, status := range m.states {
		if key.direction == LocalToRemote && status.State == ForwardActive {
			activePublishedPorts[status.RemotePort] = struct{}{}
		}
	}
	listeners := make([]Listener, 0, len(m.listeners))
	for port, listener := range m.listeners {
		if _, published := activePublishedPorts[port]; !published {
			listeners = append(listeners, listener)
		}
	}
	slices.SortFunc(listeners, func(left, right Listener) int {
		return int(left.Port) - int(right.Port)
	})
	forwards := make([]ForwardStatus, 0, len(m.states))
	for _, status := range m.states {
		forwards = append(forwards, status)
	}
	slices.SortFunc(forwards, compareForwardStatus)
	return Status{
		Host:                  m.host,
		Discovery:             m.discovery,
		Listeners:             listeners,
		Forwards:              forwards,
		WorkingDirectoryRules: slices.Clone(m.workingDirectoryRules),
	}, nil
}

func compareForwardStatus(left, right ForwardStatus) int {
	if left.Direction != right.Direction {
		if left.Direction == RemoteToLocal {
			return -1
		}
		return 1
	}
	if left.Direction == LocalToRemote {
		return int(left.LocalPort) - int(right.LocalPort)
	}
	return int(left.RemotePort) - int(right.RemotePort)
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
	m.remembered = intent.RememberedForwards
	m.published = intent.PublishedForwards
	m.reservedLocalPorts = reservedLocalPorts(intent.PublishedForwards)
	m.workingDirectoryRules = intent.WorkingDirectoryRules
	m.reconcileForwardsLocked()
	return nil
}

func (m *manager) observe() {
	for {
		m.setDiscovery(DiscoveryConnecting, "")
		err := m.backend.Observe(m.ctx, m.host, m.setListeners)
		if m.ctx.Err() != nil {
			return
		}
		m.setDiscovery(DiscoveryFailed, ErrorDiagnostic(err))
		if !wait(m.ctx, m.retryDelay) {
			return
		}
	}
}

func (m *manager) setDiscovery(state DiscoveryState, diagnostic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.discovery = DiscoveryStatus{State: state, Diagnostic: diagnostic}
	}
}

func (m *manager) setListeners(listeners []Listener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.listeners = make(map[uint16]Listener, len(listeners))
	for _, listener := range listeners {
		if listener.Port != 0 {
			m.listeners[listener.Port] = listener
		}
	}
	m.reconcileForwardsLocked()
	m.discovery = DiscoveryStatus{State: DiscoveryActive}
}

func (m *manager) reconcileForwardsLocked() {
	desiredForwards := buildDesiredForwards(
		m.remembered, m.published, m.listeners, m.workingDirectoryRules,
	)
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
		m.states[desired.key()] = forwardStatus(
			desired, ForwardStarting, "", desired.preferred,
		)
	}
	for _, desired := range plan.start {
		m.startForwardLocked(desired)
	}
}

func (m *manager) startForwardLocked(desired desiredForward) {
	ctx, cancel := context.WithCancel(m.ctx)
	worker := &forwardWorker{ctx: ctx, cancel: cancel, desired: desired}
	key := desired.key()
	m.forwardWorkers[key] = worker
	m.states[key] = forwardStatus(desired, ForwardStarting, "", desired.preferred)
	m.tasks.Go(func() { m.runForward(worker) })
}

func (m *manager) runForward(worker *forwardWorker) {
	key := worker.desired.key()
	defer m.forwardStopped(key, worker)
	for {
		err := m.forwardOnce(worker)
		if worker.ctx.Err() != nil {
			return
		}
		m.setForwardState(key, worker, ForwardFailed, ErrorDiagnostic(err), worker.desired.preferred)
		if !wait(worker.ctx, m.retryDelay) {
			return
		}
	}
}

func (m *manager) forwardOnce(worker *forwardWorker) error {
	preferred := worker.desired.preferred
	key := worker.desired.key()
	maximumOffset := 0
	if preferred.Direction == RemoteToLocal && worker.desired.allowFallback {
		maximumOffset = maxTemporaryPortOffset
	}
	var lastErr error
	for offset := 0; offset <= maximumOffset; offset++ {
		if int(preferred.LocalPort)+offset > maximumTCPPort {
			break
		}
		candidate := preferred
		candidate.LocalPort += uint16(offset)
		if candidate.Direction == RemoteToLocal && m.localPortReserved(candidate.LocalPort) {
			lastErr = &BackendError{Diagnostic: "local_port_reserved"}
			continue
		}
		m.setForwardState(key, worker, ForwardStarting, "", candidate)
		err := m.backend.Forward(worker.ctx, m.host, candidate, func() {
			m.setForwardState(key, worker, ForwardActive, "", candidate)
		})
		if worker.ctx.Err() != nil {
			return worker.ctx.Err()
		}
		lastErr = err
		if !worker.desired.allowFallback || ErrorDiagnostic(err) != "local_port_conflict" {
			return err
		}
	}
	return lastErr
}

func (m *manager) localPortReserved(port uint16) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, found := m.reservedLocalPorts[port]
	return found
}

func (m *manager) forwardStopped(key forwardKey, worker *forwardWorker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forwardWorkers[key] != worker {
		return
	}
	delete(m.forwardWorkers, key)
	delete(m.states, key)
	if m.closed {
		return
	}
	m.reconcileForwardsLocked()
}

func (m *manager) setForwardState(
	key forwardKey,
	worker *forwardWorker,
	state ForwardState,
	diagnostic string,
	target ForwardTarget,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || worker.ctx.Err() != nil || m.forwardWorkers[key] != worker {
		return
	}
	status := forwardStatus(worker.desired, state, diagnostic, target)
	status.Automatic = m.states[key].Automatic
	m.states[key] = status
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
