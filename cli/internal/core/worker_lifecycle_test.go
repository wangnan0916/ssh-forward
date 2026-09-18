package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRemoveFailedForwardDoesNotWaitForRetry(t *testing.T) {
	backend := newFakeBackend()
	backend.forwardError = func(ForwardTarget) error { return errors.New("offline") }
	manager := testManager(t, backend, ForwardingIntent{RememberedForwards: []RememberedForward{{RemotePort: 8080}}}, 30*time.Second)
	eventually(t, func() bool {
		states := managerStatus(t, manager).Forwards
		return len(states) == 1 && states[0].State == ForwardFailed
	})
	require.NoError(t, manager.UpdateIntent(context.Background(), ForwardingIntent{}))
	eventually(t, func() bool { return len(managerStatus(t, manager).Forwards) == 0 })
}
