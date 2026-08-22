package jsonrpc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
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

func TestDialWatchBuffersNotificationSentBeforeSubscribeResponse(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		reader := bufio.NewReader(serverConn)
		versionID := readRequestID(t, reader)
		writeJSONFrame(t, serverConn, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"version":1}}`, versionID))
		watchID := readRequestID(t, reader)
		writeJSONFrame(t, serverConn, `{"jsonrpc":"2.0","method":"manager.snapshot","params":{"watch_id":"watch-1","snapshot":{"revision":2}}}`)
		writeJSONFrame(t, serverConn, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"watch_id":"watch-1","snapshot":{"revision":1}}}`, watchID))
		_, _ = reader.ReadByte()
	}()

	client, err := jsonrpc.DialConn(t.Context(), clientConn)
	if err != nil {
		t.Fatalf("DialConn: %v", err)
	}
	stream, err := client.Watch(t.Context())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	first, err := stream.Next(t.Context())
	if err != nil || first.Revision != 1 {
		t.Fatalf("first = %#v, %v; want revision 1", first, err)
	}
	second, err := stream.Next(t.Context())
	if err != nil || second.Revision != 2 {
		t.Fatalf("second = %#v, %v; want buffered revision 2", second, err)
	}
	_ = client.Close(context.Background())
	_ = serverConn.Close()
	<-serverDone
}

func TestDialRejectsProtocolVersionMismatch(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		reader := bufio.NewReader(serverConn)
		versionID := readRequestID(t, reader)
		writeJSONFrame(t, serverConn, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"version":2}}`, versionID))
	}()

	_, err := jsonrpc.DialConn(t.Context(), clientConn)
	if err == nil || !strings.Contains(err.Error(), "protocol 2, want 1") {
		t.Fatalf("DialConn error = %v, want protocol mismatch", err)
	}
	_ = serverConn.Close()
	<-serverDone
}

func readRequestID(t *testing.T, reader *bufio.Reader) json.RawMessage {
	t.Helper()
	frame, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	var request struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(frame, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return request.ID
}

func writeJSONFrame(t *testing.T, conn net.Conn, frame string) {
	t.Helper()
	if _, err := conn.Write(append([]byte(frame), '\n')); err != nil {
		t.Fatalf("write response: %v", err)
	}
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
