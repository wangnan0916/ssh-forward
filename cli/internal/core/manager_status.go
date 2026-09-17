package core

import (
	"context"
	"maps"
	"slices"
)

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
	forwards := slices.AppendSeq(make([]ForwardStatus, 0, len(m.states)), maps.Values(m.states))
	slices.SortFunc(forwards, compareForwardStatus)
	return Status{
		Host:                  m.host,
		Discovery:             m.discovery,
		Listeners:             listeners,
		Forwards:              forwards,
		WorkingDirectoryRules: slices.Clone(m.intent.WorkingDirectoryRules),
	}, nil
}

func compareForwardStatus(left, right ForwardStatus) int {
	return compareForwardKeys(keyFor(left.Direction, left.LocalPort, left.RemotePort), keyFor(right.Direction, right.LocalPort, right.RemotePort))
}
