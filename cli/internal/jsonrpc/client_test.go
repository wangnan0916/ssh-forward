package jsonrpc

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestSocketStreamCoalescesUnreadSnapshots(t *testing.T) {
	stream := newSocketStream(&managerClient{}, "watch-1", []byte(`{"revision":1}`))
	stream.push([]byte(`{"revision":2}`))
	stream.push([]byte(`{"revision":4}`))
	first, err := stream.Next(context.Background())
	if err != nil || first.Revision != 1 {
		t.Fatalf("first = %#v, %v; want revision 1", first, err)
	}
	second, err := stream.Next(context.Background())
	if err != nil || second.Revision != 4 {
		t.Fatalf("second = %#v, %v; want coalesced revision 4", second, err)
	}
}

func TestSocketStreamRejectsConcurrentNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := &socketStream{ready: make(chan struct{}, 1)}
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
