package core

import (
	"context"
	"slices"
	"sync"
	"time"
)

const defaultRetryDelay = time.Second

type managerOptions struct {
	host       HostAlias
	backend    Backend
	ports      []uint16
	retryDelay time.Duration
}

type manager struct {
	mu      sync.RWMutex
	closed  bool
	host    HostAlias
	backend Backend

	discovery DiscoveryStatus
	listeners map[uint16]struct{}
	states    map[uint16]ForwardStatus

	retryDelay time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
}

// NewManager observes host and keeps every remembered port forwarded. OpenSSH
// holds the local listener even when the remote process is temporarily absent.
func NewManager(host HostAlias, backend Backend, ports []uint16) Manager {
	return newManager(managerOptions{host: host, backend: backend, ports: ports})
}

func newManager(options managerOptions) *manager {
	ctx, cancel := context.WithCancel(context.Background())
	if options.retryDelay <= 0 {
		options.retryDelay = defaultRetryDelay
	}
	ports := normalizedManagerPorts(options.ports)
	m := &manager{
		host:       options.host,
		backend:    options.backend,
		discovery:  DiscoveryStatus{State: DiscoveryConnecting},
		listeners:  make(map[uint16]struct{}),
		states:     make(map[uint16]ForwardStatus),
		retryDelay: options.retryDelay,
		ctx:        ctx,
		cancel:     cancel,
	}
	for _, port := range ports {
		m.states[port] = ForwardStatus{Port: port, State: ForwardStarting}
	}
	if m.backend == nil || m.host == "" {
		m.discovery = DiscoveryStatus{State: DiscoveryFailed, Diagnostic: "not_configured"}
		for port := range m.states {
			m.states[port] = ForwardStatus{Port: port, State: ForwardFailed, Diagnostic: "not_configured"}
		}
		return m
	}

	m.workers.Add(1)
	go m.observe()
	for _, port := range ports {
		m.workers.Add(1)
		go m.runForward(port)
	}
	return m
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
		Host:      m.host,
		Discovery: m.discovery,
		Listeners: listeners,
		Forwards:  forwards,
	}, nil
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
}

func (m *manager) runForward(port uint16) {
	defer m.workers.Done()
	for {
		m.setForwardState(port, ForwardStarting, "")
		err := m.backend.Forward(m.ctx, m.host, port, func() {
			m.setForwardState(port, ForwardActive, "")
		})
		if m.ctx.Err() != nil {
			return
		}
		m.setForwardState(port, ForwardFailed, ErrorDiagnostic(err))
		if !wait(m.ctx, m.retryDelay) {
			return
		}
	}
}

func (m *manager) setForwardState(port uint16, state ForwardState, diagnostic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
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
