package core

import (
	"context"
	"slices"
	"sync"
	"time"
)

const (
	defaultConfigPoll = 500 * time.Millisecond
	defaultRetryDelay = time.Second
)

type managerOptions struct {
	host       HostAlias
	backend    Backend
	ports      PortSource
	configPoll time.Duration
	retryDelay time.Duration
}

type manager struct {
	mu      sync.RWMutex
	closed  bool
	host    HostAlias
	backend Backend
	ports   PortSource

	discovery        DiscoveryStatus
	configDiagnostic string
	desired          map[uint16]struct{}
	listeners        map[uint16]struct{}
	states           map[uint16]ForwardStatus
	runs             map[uint16]*forwardRun

	configPoll time.Duration
	retryDelay time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
}

type forwardRun struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func NewManager(host HostAlias, backend Backend, ports PortSource) Manager {
	return newManager(managerOptions{host: host, backend: backend, ports: ports})
}

func newManager(options managerOptions) *manager {
	ctx, cancel := context.WithCancel(context.Background())
	if options.configPoll <= 0 {
		options.configPoll = defaultConfigPoll
	}
	if options.retryDelay <= 0 {
		options.retryDelay = defaultRetryDelay
	}
	if options.ports == nil {
		options.ports = func() ([]uint16, error) { return nil, nil }
	}
	m := &manager{
		host:       options.host,
		backend:    options.backend,
		ports:      options.ports,
		discovery:  DiscoveryStatus{State: DiscoveryConnecting},
		desired:    make(map[uint16]struct{}),
		listeners:  make(map[uint16]struct{}),
		states:     make(map[uint16]ForwardStatus),
		runs:       make(map[uint16]*forwardRun),
		configPoll: options.configPoll,
		retryDelay: options.retryDelay,
		ctx:        ctx,
		cancel:     cancel,
	}
	m.refreshPorts()
	if m.backend == nil || m.host == "" {
		m.discovery = DiscoveryStatus{State: DiscoveryFailed, Diagnostic: "not_configured"}
	}
	m.workers.Add(1)
	go m.pollPorts()
	if m.backend == nil || m.host == "" {
		return m
	}
	m.workers.Add(1)
	go m.observe()
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
	listeners := make([]uint16, 0, len(m.listeners))
	for port := range m.listeners {
		listeners = append(listeners, port)
	}
	slices.Sort(listeners)
	forwards := make([]ForwardStatus, 0, len(m.states))
	for _, status := range m.states {
		forwards = append(forwards, status)
	}
	slices.SortFunc(forwards, func(left, right ForwardStatus) int {
		return int(left.Port) - int(right.Port)
	})
	return Status{
		Host:             m.host,
		Discovery:        m.discovery,
		Listeners:        listeners,
		Forwards:         forwards,
		ConfigDiagnostic: m.configDiagnostic,
	}, nil
}

func (m *manager) refreshPorts() {
	ports, err := m.ports()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if err != nil {
		m.configDiagnostic = "config_file_invalid"
		return
	}
	m.configDiagnostic = ""
	next := make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		if port != 0 {
			next[port] = struct{}{}
		}
	}
	for port := range m.desired {
		if _, found := next[port]; found {
			continue
		}
		m.stopForwardLocked(port)
		delete(m.states, port)
	}
	for port := range next {
		if _, found := m.desired[port]; !found {
			m.states[port] = ForwardStatus{Port: port, State: ForwardWaiting}
		}
	}
	m.desired = next
	m.reconcileLocked()
}

func (m *manager) pollPorts() {
	defer m.workers.Done()
	ticker := time.NewTicker(m.configPoll)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.refreshPorts()
		}
	}
}

func (m *manager) observe() {
	defer m.workers.Done()
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

func (m *manager) setListeners(ports []uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.listeners = make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		if port != 0 {
			m.listeners[port] = struct{}{}
		}
	}
	m.discovery = DiscoveryStatus{State: DiscoveryActive}
	m.reconcileLocked()
}

func (m *manager) reconcileLocked() {
	for port := range m.desired {
		_, listening := m.listeners[port]
		_, running := m.runs[port]
		switch {
		case listening && !running:
			m.startForwardLocked(port)
		case !listening && running:
			m.stopForwardLocked(port)
			m.states[port] = ForwardStatus{Port: port, State: ForwardWaiting}
		case !listening:
			m.states[port] = ForwardStatus{Port: port, State: ForwardWaiting}
		}
	}
}

func (m *manager) startForwardLocked(port uint16) {
	ctx, cancel := context.WithCancel(m.ctx)
	run := &forwardRun{ctx: ctx, cancel: cancel}
	m.runs[port] = run
	m.states[port] = ForwardStatus{Port: port, State: ForwardStarting}
	m.workers.Add(1)
	go m.runForward(port, run)
}

func (m *manager) stopForwardLocked(port uint16) {
	if run := m.runs[port]; run != nil {
		delete(m.runs, port)
		run.cancel()
	}
}

func (m *manager) runForward(port uint16, run *forwardRun) {
	defer m.workers.Done()
	for {
		m.setForwardState(port, run, ForwardStarting, "")
		err := m.backend.Forward(run.ctx, m.host, port, func() {
			m.setForwardState(port, run, ForwardActive, "")
		})
		if run.ctx.Err() != nil {
			return
		}
		m.setForwardState(port, run, ForwardFailed, ErrorDiagnostic(err))
		if !wait(run.ctx, m.retryDelay) {
			return
		}
	}
}

func (m *manager) setForwardState(port uint16, run *forwardRun, state ForwardState, diagnostic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.runs[port] == run {
		m.states[port] = ForwardStatus{Port: port, State: state, Diagnostic: diagnostic}
	}
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
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		for port := range m.runs {
			m.stopForwardLocked(port)
		}
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
