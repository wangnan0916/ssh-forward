package jsonrpc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	managerjsonrpc "github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

type snapshotManager struct {
	snapshot core.Snapshot
}

func (m *snapshotManager) Snapshot(context.Context) (core.Snapshot, error) {
	return m.snapshot, nil
}

func (m *snapshotManager) Watch(context.Context) (core.SnapshotStream, error) {
	return nil, errors.New("unexpected Watch call")
}

func (*snapshotManager) Close(context.Context) error {
	return nil
}

type testSession struct {
	client   net.Conn
	server   net.Conn
	reader   *bufio.Reader
	manager  core.Manager
	cancel   context.CancelFunc
	done     <-chan error
	waitOnce sync.Once
	waitErr  error
}

func newTestSession(t *testing.T) *testSession {
	t.Helper()
	return newTestSessionWithManager(t, core.NewManager())
}

func newTestSessionWithManager(t *testing.T, manager core.Manager) *testSession {
	t.Helper()
	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- managerjsonrpc.ServeConn(ctx, server, manager)
	}()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	session := &testSession{
		client:  client,
		server:  server,
		reader:  bufio.NewReader(client),
		manager: manager,
		cancel:  cancel,
		done:    done,
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = server.Close()
		if err := session.wait(); err != nil {
			t.Errorf("serve JSON-RPC session: %v", err)
		}
		if err := manager.Close(context.Background()); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return session
}

func (s *testSession) wait() error {
	s.waitOnce.Do(func() {
		select {
		case s.waitErr = <-s.done:
		case <-time.After(time.Second):
			s.waitErr = errors.New("JSON-RPC session did not stop")
		}
	})
	return s.waitErr
}

func (s *testSession) exchange(t *testing.T, request string) []byte {
	t.Helper()
	if _, err := s.client.Write(append([]byte(request), '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := s.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response
}

func TestServeReportsProtocolVersion(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.version"}`)
	assertJSONEqual(t, response, []byte(`{"jsonrpc":"2.0","id":"1","result":{"version":1}}`))
}

func TestServeSnapshotDoesNotRequireHandshakeOrScope(t *testing.T) {
	manager := &snapshotManager{snapshot: core.Snapshot{Revision: 42}}
	session := newTestSessionWithManager(t, manager)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.snapshot"}`)
	assertJSONEqual(t, response, []byte(`{"jsonrpc":"2.0","id":"1","result":{"snapshot":{"revision":42}}}`))
}

func TestSharedGoldenTranscripts(t *testing.T) {
	forward := core.ForwardSnapshot{
		ID:                 core.ForwardID("managed:ipv4:loopback:8080"),
		RemotePort:         8080,
		RemoteFamily:       core.FamilyIPv4,
		AllocatedLocalPort: 8081,
		LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
	}
	tests := []struct {
		name    string
		manager core.Manager
	}{
		{name: "version.jsonl", manager: core.NewManager()},
		{name: "snapshot-empty.jsonl", manager: core.NewManager()},
		{name: "snapshot-discovery.jsonl", manager: &snapshotManager{snapshot: discoveryFixtureSnapshot()}},
		{
			name: "watch-limit.jsonl",
			manager: &watchErrorManager{
				snapshotManager: &snapshotManager{},
				err:             &core.DomainError{Kind: core.ErrorWatchLimit, Retryable: true},
			},
		},
		{
			name: "watch-start.jsonl",
			manager: &watchManager{
				snapshotManager: &snapshotManager{},
				stream:          newScriptedSnapshotStream(core.Snapshot{Revision: 3}),
			},
		},
		{
			name: "watch-unwatch.jsonl",
			manager: &watchManager{
				snapshotManager: &snapshotManager{},
				stream:          newScriptedSnapshotStream(core.Snapshot{Revision: 3}),
			},
		},
		{
			name: "snapshot-managed-forward.jsonl",
			manager: &snapshotManager{snapshot: core.Snapshot{
				Revision: 9,
				Host: &core.HostSnapshot{
					Alias:      core.HostAlias("development"),
					Connection: core.ConnectionConnected,
					Discovery: core.DiscoverySnapshot{
						State: core.DiscoveryStarting,
						Capability: core.DiscoveryCapability{
							RemoteListeners: core.CapabilityUnavailable,
							SocketIdentity:  core.CapabilityUnavailable,
							ProcessMetadata: core.CapabilityUnavailable,
						},
					},
					ListenerObservations: []core.ListenerObservation{},
					Forwards:             []core.ForwardSnapshot{forward},
				},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "test", "protocol", "v1", test.name)
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(contents), []byte{'\n'})
			if len(lines)%2 != 0 {
				t.Fatalf("golden transcript has %d lines, want request/response pairs", len(lines))
			}
			session := newTestSessionWithManager(t, test.manager)
			for index := 0; index < len(lines); index += 2 {
				response := session.exchange(t, string(lines[index]))
				assertJSONEqual(t, response, lines[index+1])
			}
		})
	}
}

func discoveryFixtureSnapshot() core.Snapshot {
	return core.Snapshot{
		Revision: 9,
		Host: &core.HostSnapshot{
			Alias:      core.HostAlias("development"),
			Connection: core.ConnectionConnected,
			Discovery: core.DiscoverySnapshot{
				State: core.DiscoveryDegraded,
				Capability: core.DiscoveryCapability{
					RemoteListeners: core.CapabilityFull,
					SocketIdentity:  core.CapabilityFull,
					ProcessMetadata: core.CapabilityPartial,
				},
				BaselineEstablished: true,
				ScannerVersion:      1,
				ScannerChecksum:     "abc123",
				Diagnostic:          "process_metadata_partial",
			},
			ListenerObservations: []core.ListenerObservation{{
				Family:           core.FamilyIPv4,
				BindScope:        core.BindLoopback,
				RemotePort:       8080,
				SocketIdentities: []core.SocketIdentity{core.SocketIdentity("socket:one")},
				Processes: []core.ProcessChain{{Processes: []core.ProcessMetadata{{
					PID:              42,
					Executable:       "/usr/bin/python3",
					WorkingDirectory: "/workspace",
					Arguments:        []string{"python3", "app.py"},
				}}}},
			}},
			Forwards: []core.ForwardSnapshot{},
		},
	}
}

func TestServeReturnsCompleteManagerSnapshot(t *testing.T) {
	snapshot := discoveryFixtureSnapshot()
	snapshot.Host.Forwards = []core.ForwardSnapshot{{
		ID:                 core.ForwardID("managed:ipv4:loopback:8080"),
		RemotePort:         8080,
		RemoteFamily:       core.FamilyIPv4,
		AllocatedLocalPort: 8081,
		LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
	}}
	session := newTestSessionWithManager(t, &snapshotManager{snapshot: snapshot})
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.snapshot"}`)
	want := `{"jsonrpc":"2.0","id":"1","result":{"snapshot":{"revision":9,"host":{"alias":"development","connection":"connected","discovery":{"state":"degraded","capability":{"remote_listeners":"full","socket_identity":"full","process_metadata":"partial"},"baseline_established":true,"scanner_version":1,"scanner_checksum":"abc123","diagnostic":"process_metadata_partial"},"listener_observations":[{"family":"ipv4","bind_scope":"loopback","remote_port":8080,"socket_identities":["socket:one"],"process_chains":[{"processes":[{"pid":42,"executable":"/usr/bin/python3","working_directory":"/workspace","arguments":["python3","app.py"]}]}]}],"forwards":[{"id":"managed:ipv4:loopback:8080","remote_port":8080,"remote_family":"ipv4","allocated_local_port":8081,"local_families":["ipv4","ipv6"]}]}}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeLetsJRPC2HandleUnknownMethods(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"unknown"}`)
	want := `{"jsonrpc":"2.0","id":"1","error":{"code":-32601,"message":"method not found","data":"unknown"}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeClosesOnOversizedFrame(t *testing.T) {
	session := newTestSession(t)
	oversized := append(bytes.Repeat([]byte(" "), (1<<20)+1), '\n')
	_, writeErr := session.client.Write(oversized)
	if writeErr == nil {
		var oneByte [1]byte
		if _, err := session.client.Read(oneByte[:]); err == nil {
			t.Fatal("connection remained open after oversized frame")
		}
	}
	if err := session.wait(); err != nil {
		t.Fatalf("serve oversized frame: %v", err)
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode response %q: %v", got, err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode expected response: %v", err)
	}
	if diff := cmp.Diff(gotValue, wantValue); diff != "" {
		t.Fatalf("response mismatch (-got +want):\n%s", diff)
	}
}
