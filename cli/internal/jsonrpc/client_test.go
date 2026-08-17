package jsonrpc

import (
	"context"
	"testing"
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
