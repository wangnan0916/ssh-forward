package core

import (
	"context"
	"time"
)

type forwardWorker struct {
	ctx     context.Context
	cancel  context.CancelFunc
	desired desiredForward
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
		err := m.backend.Forward(worker.ctx, candidate, func() {
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

func (m *manager) setForwardState(key forwardKey, worker *forwardWorker, state ForwardState, diagnostic string, target ForwardTarget) {
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
