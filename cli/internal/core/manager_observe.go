package core

func (m *manager) observe() {
	for {
		m.setDiscovery(DiscoveryConnecting, "")
		err := m.backend.Observe(m.ctx, m.setListeners)
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
		if state == DiscoveryFailed {
			m.listeners = make(map[uint16]Listener)
			m.reconcileForwardsLocked()
		}
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
