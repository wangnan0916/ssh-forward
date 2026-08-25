package core

import (
	"context"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const defaultRetryDelay = time.Second

type managerOptions struct {
	host       HostAlias
	backend    Backend
	intent     ForwardingIntent
	retryDelay time.Duration
}

type forwardWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	target ForwardTarget
}

type manager struct {
	mu      sync.RWMutex
	closed  bool
	host    HostAlias
	backend Backend

	discovery             DiscoveryStatus
	listeners             map[uint16]Listener
	states                map[uint16]ForwardStatus
	remembered            []RememberedForward
	workingDirectoryRules []string
	forwardWorkers        map[uint16]*forwardWorker

	retryDelay time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	tasks      sync.WaitGroup
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
}

// NewManager observes host and reconciles remembered and automatic forwards.
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
		states:                make(map[uint16]ForwardStatus),
		remembered:            intent.RememberedForwards,
		workingDirectoryRules: intent.WorkingDirectoryRules,
		forwardWorkers:        make(map[uint16]*forwardWorker),
		retryDelay:            options.retryDelay,
		ctx:                   ctx,
		cancel:                cancel,
		closeDone:             make(chan struct{}),
	}
	if m.backend == nil || m.host == "" {
		m.discovery = DiscoveryStatus{State: DiscoveryFailed, Diagnostic: "not_configured"}
		for _, forward := range m.remembered {
			m.states[forward.RemotePort] = m.forwardStatus(
				forward.RemotePort, ForwardFailed, "not_configured",
			)
		}
		return m
	}

	m.mu.Lock()
	for _, forward := range m.remembered {
		m.startForwardLocked(forwardTarget(forward))
	}
	m.mu.Unlock()
	m.tasks.Go(m.observe)
	return m
}

func normalizedForwardingIntent(intent ForwardingIntent) ForwardingIntent {
	intent.RememberedForwards = normalizedManagerForwards(intent.RememberedForwards)
	patterns := make([]string, 0, len(intent.WorkingDirectoryRules))
	for _, pattern := range intent.WorkingDirectoryRules {
		if path.IsAbs(pattern) && doublestar.ValidatePattern(pattern) {
			patterns = append(patterns, pattern)
		}
	}
	slices.Sort(patterns)
	intent.WorkingDirectoryRules = slices.Compact(patterns)
	return intent
}

func normalizedManagerForwards(forwards []RememberedForward) []RememberedForward {
	byRemotePort := make(map[uint16]RememberedForward, len(forwards))
	for _, forward := range forwards {
		if forward.RemotePort == 0 {
			continue
		}
		forward = forward.WithDefaults()
		byRemotePort[forward.RemotePort] = forward
	}
	normalized := make([]RememberedForward, 0, len(byRemotePort))
	for _, forward := range byRemotePort {
		normalized = append(normalized, forward)
	}
	slices.SortFunc(normalized, func(left, right RememberedForward) int {
		return int(left.RemotePort) - int(right.RemotePort)
	})
	return normalized
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
	listeners := make([]Listener, 0, len(m.listeners))
	for _, listener := range m.listeners {
		listeners = append(listeners, listener)
	}
	slices.SortFunc(listeners, func(left, right Listener) int {
		return int(left.Port) - int(right.Port)
	})
	forwards := make([]ForwardStatus, 0, len(m.states))
	for _, status := range m.states {
		forwards = append(forwards, status)
	}
	slices.SortFunc(forwards, func(left, right ForwardStatus) int {
		return int(left.RemotePort) - int(right.RemotePort)
	})
	return Status{
		Host:                  m.host,
		Discovery:             m.discovery,
		Listeners:             listeners,
		Forwards:              forwards,
		WorkingDirectoryRules: slices.Clone(m.workingDirectoryRules),
	}, nil
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
	m.workingDirectoryRules = intent.WorkingDirectoryRules
	m.reconcileForwardsLocked()
	for remotePort, status := range m.states {
		updated := m.forwardStatus(remotePort, status.State, status.Diagnostic)
		if status.State == ForwardActive {
			updated.LocalPort = status.LocalPort
		}
		m.states[remotePort] = updated
	}
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
	for remotePort, worker := range m.forwardWorkers {
		desired, found := m.desiredForward(remotePort)
		if found && worker.target == desired {
			continue
		}
		worker.cancel()
		if found {
			m.states[remotePort] = m.forwardStatus(remotePort, ForwardStarting, "")
		} else {
			delete(m.states, remotePort)
		}
	}
	for _, forward := range m.remembered {
		m.ensureForwardLocked(forwardTarget(forward))
	}
	for remotePort, listener := range m.listeners {
		if !m.isRemembered(remotePort) && matchesWorkingDirectory(m.workingDirectoryRules, listener.WorkingDirectory) {
			m.ensureForwardLocked(automaticForwardTarget(remotePort))
		}
	}
}

func (m *manager) desiredForward(remotePort uint16) (ForwardTarget, bool) {
	if remembered, found := m.rememberedForward(remotePort); found {
		return forwardTarget(remembered), true
	}
	listener, found := m.listeners[remotePort]
	if found && matchesWorkingDirectory(m.workingDirectoryRules, listener.WorkingDirectory) {
		return automaticForwardTarget(remotePort), true
	}
	return ForwardTarget{}, false
}

func matchesWorkingDirectory(patterns []string, directory string) bool {
	if directory == "" || !path.IsAbs(directory) {
		return false
	}
	for _, pattern := range patterns {
		if matched, _ := doublestar.Match(pattern, directory); matched {
			return true
		}
	}
	return false
}

func (m *manager) ensureForwardLocked(target ForwardTarget) {
	if worker := m.forwardWorkers[target.RemotePort]; worker != nil {
		if worker.ctx.Err() != nil {
			m.states[target.RemotePort] = m.forwardStatus(target.RemotePort, ForwardStarting, "")
		}
		return
	}
	m.startForwardLocked(target)
}

func (m *manager) startForwardLocked(target ForwardTarget) {
	ctx, cancel := context.WithCancel(m.ctx)
	worker := &forwardWorker{ctx: ctx, cancel: cancel, target: target}
	m.forwardWorkers[target.RemotePort] = worker
	m.states[target.RemotePort] = m.forwardStatus(target.RemotePort, ForwardStarting, "")
	m.tasks.Go(func() { m.runForward(worker) })
}

func (m *manager) runForward(worker *forwardWorker) {
	remotePort := worker.target.RemotePort
	defer m.forwardStopped(remotePort, worker)
	for {
		m.setForwardState(remotePort, worker, ForwardStarting, "", 0)
		err := m.backend.Forward(worker.ctx, m.host, worker.target, func(localPort uint16) {
			m.setForwardState(remotePort, worker, ForwardActive, "", localPort)
		})
		if worker.ctx.Err() != nil {
			return
		}
		m.setForwardState(remotePort, worker, ForwardFailed, ErrorDiagnostic(err), 0)
		if !wait(worker.ctx, m.retryDelay) {
			return
		}
	}
}

func (m *manager) forwardStopped(remotePort uint16, worker *forwardWorker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forwardWorkers[remotePort] != worker {
		return
	}
	delete(m.forwardWorkers, remotePort)
	if m.closed {
		return
	}
	if target, found := m.desiredForward(remotePort); found {
		m.startForwardLocked(target)
	} else {
		delete(m.states, remotePort)
	}
}

func (m *manager) setForwardState(
	remotePort uint16,
	worker *forwardWorker,
	state ForwardState,
	diagnostic string,
	actualLocalPort uint16,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && worker.ctx.Err() == nil && m.forwardWorkers[remotePort] == worker {
		status := m.forwardStatus(remotePort, state, diagnostic)
		if state == ForwardActive {
			status.LocalPort = actualLocalPort
		}
		m.states[remotePort] = status
	}
}

func (m *manager) forwardStatus(remotePort uint16, state ForwardState, diagnostic string) ForwardStatus {
	target := automaticForwardTarget(remotePort)
	automatic := true
	if remembered, found := m.rememberedForward(remotePort); found {
		target = forwardTarget(remembered)
		automatic = false
	}
	return ForwardStatus{
		RemotePort:         target.RemotePort,
		PreferredLocalPort: target.LocalPort,
		LocalPort:          target.LocalPort,
		State:              state,
		Diagnostic:         diagnostic,
		Automatic:          automatic,
		AllowFallback:      target.AllowFallback,
	}
}

func automaticForwardTarget(port uint16) ForwardTarget {
	return ForwardTarget{RemotePort: port, LocalPort: port, AllowFallback: true}
}

func forwardTarget(forward RememberedForward) ForwardTarget {
	return ForwardTarget{
		RemotePort:    forward.RemotePort,
		LocalPort:     forward.LocalPort,
		AllowFallback: forward.AllowFallback,
	}
}

func (m *manager) isRemembered(remotePort uint16) bool {
	_, found := m.rememberedForward(remotePort)
	return found
}

func (m *manager) rememberedForward(remotePort uint16) (RememberedForward, bool) {
	index, found := slices.BinarySearchFunc(m.remembered, remotePort, func(forward RememberedForward, remotePort uint16) int {
		return int(forward.RemotePort) - int(remotePort)
	})
	if !found {
		return RememberedForward{}, false
	}
	return m.remembered[index], true
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
