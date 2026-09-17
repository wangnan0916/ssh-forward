package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRemoveFailedForwardDoesNotWaitForRetry(t *testing.T) {
	backend := newFakeBackend()
	backend.forwardError = func(ForwardTarget) error { return errors.New("offline") }
	manager := newManager(managerOptions{host: "dev", backend: backend, retryDelay: 30 * time.Second,
		intent: ForwardingIntent{RememberedForwards: []RememberedForward{{RemotePort: 8080}}}})
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	eventually(t, func() bool {
		states := managerStatus(t, manager).Forwards
		return len(states) == 1 && states[0].State == ForwardFailed
	})
	if err := manager.UpdateIntent(context.Background(), ForwardingIntent{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return len(managerStatus(t, manager).Forwards) == 0 })
}
