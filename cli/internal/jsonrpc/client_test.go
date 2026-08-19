package jsonrpc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

// TestDialSnapshotTalksToScopeAllServers pins the v1 client against managers
// that reject empty snapshot/watch params (the Aug 17 singleton). The live
// protocol still accepts `{"scope":{"kind":"all"}}` on both sides.
func TestDialSnapshotTalksToScopeAllServers(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveScopeAllPeer(serverConn) }()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client, err := jsonrpc.DialConn(ctx, clientConn)
	if err != nil {
		cancel()
		_ = serverConn.Close()
		_ = clientConn.Close()
		<-done
		t.Fatalf("DialConn: %v", err)
	}
	defer func() {
		_ = client.Close(context.Background())
		cancel()
		_ = serverConn.Close()
		_ = clientConn.Close()
		<-done
	}()
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Host == nil || string(snapshot.Host.Alias) != "ubuntu" {
		t.Fatalf("Snapshot host = %#v, want ubuntu", snapshot.Host)
	}
	stream, err := client.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stream.Close()
	watched, err := stream.Next(ctx)
	if err != nil || watched.Host == nil || string(watched.Host.Alias) != "ubuntu" {
		t.Fatalf("Watch Next = %#v, %v; want ubuntu", watched, err)
	}
}

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

type peerRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func serveScopeAllPeer(conn net.Conn) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	hello, err := readPeerRequest(reader)
	if err != nil {
		return err
	}
	if hello.Method != "system.hello" {
		return fmt.Errorf("first method %s, want system.hello", hello.Method)
	}
	if err := writePeer(conn, hello.ID, `{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"],"max_frame_bytes":1048576}`); err != nil {
		return err
	}
	host := `{"revision":1,"host":{"alias":"ubuntu","connection":"connected","discovery":{"state":"ready","capability":{"remote_listeners":"full","socket_identity":"full","process_metadata":"full"},"baseline_established":true,"scanner_version":1,"scanner_checksum":"x","diagnostic":""},"listener_observations":[],"forwards":[]}}`
	for {
		request, err := readPeerRequest(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var params struct {
			Scope struct {
				Kind string `json:"kind"`
			} `json:"scope"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.Scope.Kind != "all" {
			if err := writePeerError(conn, request.ID); err != nil {
				return err
			}
			continue
		}
		switch request.Method {
		case "manager.snapshot":
			if err := writePeer(conn, request.ID, `{"snapshot":`+host+`}`); err != nil {
				return err
			}
		case "manager.watch":
			if err := writePeer(conn, request.ID, `{"watch_id":"watch-1","snapshot":`+host+`}`); err != nil {
				return err
			}
		default:
			if err := writePeerError(conn, request.ID); err != nil {
				return err
			}
		}
	}
}

func readPeerRequest(reader *bufio.Reader) (peerRequest, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return peerRequest{}, err
	}
	var request peerRequest
	if err := json.Unmarshal(bytes.TrimSpace(line), &request); err != nil {
		return peerRequest{}, err
	}
	return request, nil
}

func writePeer(conn net.Conn, id json.RawMessage, result string) error {
	_, err := fmt.Fprintf(conn, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, result)
	return err
}

func writePeerError(conn net.Conn, id json.RawMessage) error {
	_, err := fmt.Fprintf(conn, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"invalid parameters","data":{"kind":"invalid_scope","retryable":false}}}`+"\n", id)
	return err
}
