package jsonrpc_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

func TestDialWatchCoalescesUnreadSnapshots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
		client, cleanup := dialWatchManager(t, upstream)
		defer cleanup()
		stream, err := client.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		first, err := stream.Next(t.Context())
		if err != nil || first.Revision != 1 {
			t.Fatalf("first = %#v, %v; want revision 1", first, err)
		}
		upstream.snapshots <- core.Snapshot{Revision: 2}
		upstream.snapshots <- core.Snapshot{Revision: 4}
		synctest.Wait()
		second, err := stream.Next(t.Context())
		if err != nil || second.Revision != 4 {
			t.Fatalf("second = %#v, %v; want coalesced revision 4", second, err)
		}
	})
}

func TestDialWatchRejectsConcurrentNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstream := newScriptedSnapshotStream(core.Snapshot{Revision: 1})
		client, cleanup := dialWatchManager(t, upstream)
		defer cleanup()
		stream, err := client.Watch(t.Context())
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if _, err := stream.Next(t.Context()); err != nil {
			t.Fatalf("initial Next: %v", err)
		}
		firstContext, cancelFirst := context.WithCancel(t.Context())
		firstDone := make(chan error, 1)
		go func() {
			_, err := stream.Next(firstContext)
			firstDone <- err
		}()
		synctest.Wait()
		if _, err := stream.Next(t.Context()); !errors.Is(err, core.ErrConcurrentSnapshotNext) {
			t.Fatalf("concurrent Next error = %v, want ErrConcurrentSnapshotNext", err)
		}
		cancelFirst()
		if err := <-firstDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Next error = %v, want context.Canceled", err)
		}
	})
}

func dialWatchManager(t *testing.T, stream core.SnapshotStream) (core.Manager, func()) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	manager := &watchManager{
		snapshotManager: &snapshotManager{},
		stream:          stream,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = jsonrpc.ServeConn(ctx, serverConn, manager)
	}()
	client, err := jsonrpc.DialConn(ctx, clientConn)
	if err != nil {
		cancel()
		_ = serverConn.Close()
		_ = clientConn.Close()
		<-done
		t.Fatalf("DialConn: %v", err)
	}
	return client, func() {
		_ = client.Close(context.Background())
		cancel()
		_ = serverConn.Close()
		_ = clientConn.Close()
		<-done
	}
}
