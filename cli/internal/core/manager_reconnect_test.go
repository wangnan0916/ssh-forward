package core

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type sequenceConnector struct {
	mu       sync.Mutex
	next     int
	sessions []HostSession
	releases []<-chan struct{}
	started  chan int
}

func (c *sequenceConnector) Connect(ctx context.Context, _ HostAlias) (HostSession, error) {
	c.mu.Lock()
	index := c.next
	c.next++
	c.mu.Unlock()
	c.started <- index
	select {
	case <-c.releases[index]:
		return c.sessions[index], nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type permanentFailureConnector struct {
	started chan struct{}
}

func (c permanentFailureConnector) Connect(context.Context, HostAlias) (HostSession, error) {
	close(c.started)
	return nil, &SessionError{Disposition: SessionSuspend, Reason: SessionReasonAuthentication}
}

func TestManagerSuspendsReconnectAfterPermanentSSHFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		connector := permanentFailureConnector{started: make(chan struct{})}
		manager := newManager(managerOptions{
			host:             HostAlias("development"),
			connector:        connector,
			forwardAllocator: &autoAllocator{},
			retryWait: func(context.Context, time.Duration) bool {
				t.Fatal("permanent failure unexpectedly entered retry backoff")
				return false
			},
		})
		defer func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_ = manager.Close(ctx)
		}()
		select {
		case <-connector.started:
		case <-time.After(time.Second):
			t.Fatal("connection attempt did not start")
		}
		synctest.Wait()
		snapshot, err := manager.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if got := snapshot.Host.Connection; got != ConnectionDisconnected || snapshot.Host.ConnectionDiagnostic != "authentication_failed" || snapshot.Revision != 2 {
			t.Fatalf("connection = %q diagnostic %q revision %d, want disconnected authentication_failed revision 2", got, snapshot.Host.ConnectionDiagnostic, snapshot.Revision)
		}
	})
}
