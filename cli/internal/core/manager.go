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
}

type manager struct {
	mu      sync.RWMutex
	closed  bool
	host    HostAlias
	backend Backend

	discovery             DiscoveryStatus
	listeners             map[uint16]Listener
	states                map[uint16]ForwardStatus
	remembered            []uint16
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
		remembered:            intent.RememberedPorts,
		workingDirectoryRules: intent.WorkingDirectoryRules,
		forwardWorkers:        make(map[uint16]*forwardWorker),
		retryDelay:            options.retryDelay,
		ctx:                   ctx,
		cancel:                cancel,
		closeDone:             make(chan struct{}),
	}
	if m.backend == nil || m.host == "" {
		m.discovery = DiscoveryStatus{State: DiscoveryFailed, Diagnostic: "not_configured"}
		for _, port := range m.remembered {
			m.states[port] = ForwardStatus{Port: port, State: ForwardFailed, Diagnostic: "not_configured"}
		}
		return m
	}

	m.mu.Lock()
	for _, port := range m.remembered {
		m.startForwardLocked(port)
	}
	m.mu.Unlock()
	m.tasks.Go(m.observe)
	return m
}

func normalizedForwardingIntent(intent ForwardingIntent) ForwardingIntent {
	intent.RememberedPorts = normalizedManagerPorts(intent.RememberedPorts)
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

func normalizedManagerPorts(ports []uint16) []uint16 {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	ports = slices.Compact(ports)
	if len(ports) != 0 && ports[0] == 0 {
		ports = ports[1:]
	}
	return ports
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
		return int(left.Port) - int(right.Port)
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
	m.remembered = intent.RememberedPorts
	m.workingDirectoryRules = intent.WorkingDirectoryRules
	m.reconcileForwardsLocked()
	for port, status := range m.states {
		status.Automatic = !m.isRemembered(port)
		m.states[port] = status
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
	for port, worker := range m.forwardWorkers {
		if m.wantsForward(port) {
			continue
		}
		worker.cancel()
		delete(m.states, port)
	}
	for _, port := range m.remembered {
		m.ensureForwardLocked(port)
	}
	for port, listener := range m.listeners {
		if matchesWorkingDirectory(m.workingDirectoryRules, listener.WorkingDirectory) {
			m.ensureForwardLocked(port)
		}
	}
}

func (m *manager) wantsForward(port uint16) bool {
	if m.isRemembered(port) {
		return true
	}
	listener, found := m.listeners[port]
	return found && matchesWorkingDirectory(m.workingDirectoryRules, listener.WorkingDirectory)
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

func (m *manager) ensureForwardLocked(port uint16) {
	if worker := m.forwardWorkers[port]; worker != nil {
		if worker.ctx.Err() != nil {
			m.states[port] = m.forwardStatus(port, ForwardStarting, "")
		}
		return
	}
	m.startForwardLocked(port)
}

func (m *manager) startForwardLocked(port uint16) {
	ctx, cancel := context.WithCancel(m.ctx)
	worker := &forwardWorker{ctx: ctx, cancel: cancel}
	m.forwardWorkers[port] = worker
	m.states[port] = m.forwardStatus(port, ForwardStarting, "")
	m.tasks.Go(func() { m.runForward(port, worker) })
}

func (m *manager) runForward(port uint16, worker *forwardWorker) {
	defer m.forwardStopped(port, worker)
	for {
		m.setForwardState(port, worker, ForwardStarting, "")
		err := m.backend.Forward(worker.ctx, m.host, port, func() {
			m.setForwardState(port, worker, ForwardActive, "")
		})
		if worker.ctx.Err() != nil {
			return
		}
		m.setForwardState(port, worker, ForwardFailed, ErrorDiagnostic(err))
		if !wait(worker.ctx, m.retryDelay) {
			return
		}
	}
}

func (m *manager) forwardStopped(port uint16, worker *forwardWorker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forwardWorkers[port] != worker {
		return
	}
	delete(m.forwardWorkers, port)
	if m.closed {
		return
	}
	if m.wantsForward(port) {
		m.startForwardLocked(port)
	} else {
		delete(m.states, port)
	}
}

func (m *manager) setForwardState(port uint16, worker *forwardWorker, state ForwardState, diagnostic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && worker.ctx.Err() == nil && m.forwardWorkers[port] == worker {
		m.states[port] = m.forwardStatus(port, state, diagnostic)
	}
}

func (m *manager) forwardStatus(port uint16, state ForwardState, diagnostic string) ForwardStatus {
	return ForwardStatus{Port: port, State: state, Diagnostic: diagnostic, Automatic: !m.isRemembered(port)}
}

func (m *manager) isRemembered(port uint16) bool {
	_, found := slices.BinarySearch(m.remembered, port)
	return found
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
