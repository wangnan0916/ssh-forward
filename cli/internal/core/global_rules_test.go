package core

import (
	"testing"
	"time"
)

func TestGlobalPortRuleFollowsListenerLifecycle(t *testing.T) {
	backend := newFakeBackend()
	manager := testManager(t, backend, ForwardingIntent{
		AutoForwards:       []RememberedForward{{RemotePort: 8080, LocalPort: 18080}},
		RememberedForwards: []RememberedForward{{RemotePort: 3000}},
	}, time.Millisecond)
	eventually(t, func() bool { return len(managerStatus(t, manager).Forwards) == 1 })
	backend.listeners <- []Listener{{Port: 8080}}
	eventually(t, func() bool {
		s := managerStatus(t, manager)
		return len(s.Forwards) == 2 && s.Forwards[1].Automatic && s.Forwards[1].LocalPort == 18080 && s.Forwards[1].State == ForwardActive
	})
	backend.listeners <- []Listener{}
	eventually(t, func() bool {
		s := managerStatus(t, manager)
		return len(s.Forwards) == 1 && s.Forwards[0].RemotePort == 3000
	})
}
