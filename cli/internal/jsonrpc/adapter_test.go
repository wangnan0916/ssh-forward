package jsonrpc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ssh-forward/cli/internal/core"
	managerjsonrpc "ssh-forward/cli/internal/jsonrpc"
)

type snapshotManager struct {
	snapshot core.Snapshot
	execute  func(context.Context, core.Command) (core.Outcome, error)
}

func (m *snapshotManager) Execute(ctx context.Context, command core.Command) (core.Outcome, error) {
	if m.execute == nil {
		return core.Outcome{}, errors.New("unexpected Execute call")
	}
	return m.execute(ctx, command)
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

type blockingManager struct {
	started chan struct{}
}

func (*blockingManager) Execute(context.Context, core.Command) (core.Outcome, error) {
	return core.Outcome{}, errors.New("unexpected Execute call")
}

func (m *blockingManager) Snapshot(ctx context.Context) (core.Snapshot, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return core.Snapshot{}, ctx.Err()
}

func (*blockingManager) Watch(context.Context) (core.SnapshotStream, error) {
	return nil, errors.New("unexpected Watch call")
}

func (*blockingManager) Close(context.Context) error {
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
		done <- managerjsonrpc.Serve(ctx, server, manager)
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

func TestServeNegotiatesHello(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["cancel-v1","watch-snapshot-v1"]}}`)
	want := `{"jsonrpc":"2.0","id":"1","result":{"protocol":{"major":1,"minor":0},"capabilities":["watch-snapshot-v1"],"max_frame_bytes":1048576}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestSharedGoldenTranscripts(t *testing.T) {
	forward := core.ForwardSnapshot{
		ID:                 core.ForwardID("manual:operation-1"),
		Kind:               core.ForwardManual,
		RemotePort:         8080,
		RemoteFamily:       core.FamilyIPv4,
		AllocatedLocalPort: 8081,
		LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
	}
	tests := []struct {
		name    string
		manager core.Manager
	}{
		{name: "hello-success.jsonl", manager: core.NewManager()},
		{name: "snapshot-empty.jsonl", manager: core.NewManager()},
		{
			name: "manual-forward-add.jsonl",
			manager: &snapshotManager{execute: func(context.Context, core.Command) (core.Outcome, error) {
				return core.Outcome{Kind: core.OutcomeForwardAdded, Revision: 7, Forward: forward}, nil
			}},
		},
		{
			name: "manual-forward-remove.jsonl",
			manager: &snapshotManager{execute: func(context.Context, core.Command) (core.Outcome, error) {
				return core.Outcome{Kind: core.OutcomeForwardRemoved, Revision: 8, Forward: forward}, nil
			}},
		},
		{
			name: "snapshot-discovery.jsonl",
			manager: &snapshotManager{
				snapshot: discoveryFixtureSnapshot(),
			},
		},
		{name: "watch-capability-required.jsonl", manager: core.NewManager()},
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
			name: "snapshot-manual-forward.jsonl",
			manager: &snapshotManager{
				snapshot: core.Snapshot{
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
				},
			},
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

func TestServeCancellationStopsHelloNotification(t *testing.T) {
	session := newTestSession(t)
	notification := `{"jsonrpc":"2.0","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}` + "\n"
	if _, err := session.client.Write([]byte(notification)); err != nil {
		t.Fatalf("write hello notification: %v", err)
	}
	session.cancel()
	if err := session.wait(); err != nil {
		t.Fatalf("stop session after hello notification: %v", err)
	}
}

func TestServeRejectsInvalidHelloEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		request string
		want    string
	}{
		{
			name:    "well-formed scalar",
			request: `true`,
			want:    `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`,
		},
		{
			name:    "boolean request ID",
			request: `{"jsonrpc":"2.0","id":true,"method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`,
			want:    `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`,
		},
		{
			name:    "mixed response fields",
			request: `{"jsonrpc":"2.0","id":"1","method":"system.hello","result":{},"params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`,
			want:    `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`,
		},
		{
			name:    "missing protocol",
			request: `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"capabilities":[]}}`,
			want:    `{"jsonrpc":"2.0","id":"1","error":{"code":-32602,"message":"invalid parameters","data":{"kind":"invalid_parameters","retryable":false}}}`,
		},
		{
			name:    "null protocol",
			request: `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":null,"capabilities":[]}}`,
			want:    `{"jsonrpc":"2.0","id":"1","error":{"code":-32602,"message":"invalid parameters","data":{"kind":"invalid_parameters","retryable":false}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newTestSession(t)
			response := session.exchange(t, test.request)
			assertJSONEqual(t, response, []byte(test.want))
			assertConnectionClosed(t, session)
		})
	}
}

func TestServeRejectsBuiltinMethodBeforeHello(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"rpc.serverInfo"}`)
	want := `{"jsonrpc":"2.0","id":"1","error":{"code":-32001,"message":"system.hello is required before manager methods","data":{"kind":"hello_required","retryable":false}}}`
	assertJSONEqual(t, response, []byte(want))
	assertConnectionClosed(t, session)
}

func TestServeExecutesAddManualForward(t *testing.T) {
	wantCommand := core.AddManualForward{
		CommandID:  core.CommandID("operation-1"),
		Host:       core.HostAlias("development"),
		RemotePort: 8080,
		Family:     core.FamilyAuto,
	}
	manager := &snapshotManager{
		execute: func(_ context.Context, command core.Command) (core.Outcome, error) {
			if !reflect.DeepEqual(command, wantCommand) {
				return core.Outcome{}, fmt.Errorf("command = %#v, want %#v", command, wantCommand)
			}
			return core.Outcome{
				Kind:     core.OutcomeForwardAdded,
				Revision: 7,
				Forward: core.ForwardSnapshot{
					ID:                 core.ForwardID("manual:operation-1"),
					Kind:               core.ForwardManual,
					RemotePort:         8080,
					RemoteFamily:       core.FamilyIPv4,
					AllocatedLocalPort: 8081,
					LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
				},
			}, nil
		},
	}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.execute","params":{"command":{"kind":"manual_forward.add","operation_id":"operation-1","host":"development","remote_port":8080,"family":"auto"}}}`)
	want := `{"jsonrpc":"2.0","id":"2","result":{"outcome":{"kind":"forward_added","revision":7,"forward":{"id":"manual:operation-1","kind":"manual","remote_port":8080,"remote_family":"ipv4","allocated_local_port":8081,"local_families":["ipv4","ipv6"]}}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeExecutesRemoveForward(t *testing.T) {
	wantCommand := core.RemoveForward{
		CommandID: core.CommandID("operation-2"),
		ForwardID: core.ForwardID("manual:operation-1"),
	}
	manager := &snapshotManager{
		execute: func(_ context.Context, command core.Command) (core.Outcome, error) {
			if !reflect.DeepEqual(command, wantCommand) {
				return core.Outcome{}, fmt.Errorf("command = %#v, want %#v", command, wantCommand)
			}
			return core.Outcome{
				Kind:     core.OutcomeForwardRemoved,
				Revision: 8,
				Forward: core.ForwardSnapshot{
					ID:                 core.ForwardID("manual:operation-1"),
					Kind:               core.ForwardManual,
					RemotePort:         8080,
					RemoteFamily:       core.FamilyIPv4,
					AllocatedLocalPort: 8081,
					LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
				},
			}, nil
		},
	}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.execute","params":{"command":{"kind":"manual_forward.remove","operation_id":"operation-2","forward_id":"manual:operation-1"}}}`)
	want := `{"jsonrpc":"2.0","id":"2","result":{"outcome":{"kind":"forward_removed","revision":8,"forward":{"id":"manual:operation-1","kind":"manual","remote_port":8080,"remote_family":"ipv4","allocated_local_port":8081,"local_families":["ipv4","ipv6"]}}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeRejectsInvalidManualForwardParameters(t *testing.T) {
	requests := []string{
		`{"kind":"manual_forward.add","operation_id":"operation-1","host":"development","remote_port":0,"family":"auto"}`,
		`{"kind":"manual_forward.add","operation_id":"operation-1","host":"development","remote_port":8080,"family":"unknown"}`,
		`{"kind":"manual_forward.add","operation_id":"operation-1","host":"","remote_port":8080,"family":"auto"}`,
		fmt.Sprintf(`{"kind":"manual_forward.add","operation_id":"%s","host":"development","remote_port":8080,"family":"auto"}`, strings.Repeat("x", 129)),
		fmt.Sprintf(`{"kind":"manual_forward.add","operation_id":"operation-1","host":"%s","remote_port":8080,"family":"auto"}`, strings.Repeat("h", 256)),
		fmt.Sprintf(`{"kind":"manual_forward.remove","operation_id":"operation-2","forward_id":"%s"}`, strings.Repeat("f", 257)),
	}
	for _, command := range requests {
		session := newTestSession(t)
		session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
		response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.execute","params":{"command":`+command+`}}`)
		want := `{"jsonrpc":"2.0","id":"2","error":{"code":-32602,"message":"invalid parameters","data":{"kind":"invalid_parameters","retryable":false}}}`
		assertJSONEqual(t, response, []byte(want))
	}
}

func TestServeMapsTypedManagerError(t *testing.T) {
	manager := &snapshotManager{
		execute: func(context.Context, core.Command) (core.Outcome, error) {
			return core.Outcome{}, &core.DomainError{Kind: core.ErrorUnknownHost}
		},
	}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.execute","params":{"command":{"kind":"manual_forward.add","operation_id":"operation-1","host":"unknown","remote_port":8080,"family":"auto"}}}`)
	want := `{"jsonrpc":"2.0","id":"2","error":{"code":-32010,"message":"unknown Development Host","data":{"kind":"unknown_host","retryable":false}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeReturnsManagerSnapshotAfterHello(t *testing.T) {
	manager := &snapshotManager{
		snapshot: core.Snapshot{Revision: 42},
	}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.snapshot","params":{"scope":{"kind":"all"}}}`)
	want := `{"jsonrpc":"2.0","id":"2","result":{"snapshot":{"revision":42,"hosts":[]}}}`
	assertJSONEqual(t, response, []byte(want))
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
		ID:                 core.ForwardID("manual:operation-1"),
		Kind:               core.ForwardManual,
		RemotePort:         8080,
		RemoteFamily:       core.FamilyIPv4,
		AllocatedLocalPort: 8081,
		LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
	}}
	manager := &snapshotManager{snapshot: snapshot}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.snapshot","params":{"scope":{"kind":"all"}}}`)
	want := `{"jsonrpc":"2.0","id":"2","result":{"snapshot":{"revision":9,"hosts":[{"alias":"development","connection":"connected","discovery":{"state":"degraded","capability":{"remote_listeners":"full","socket_identity":"full","process_metadata":"partial"},"baseline_established":true,"scanner_version":1,"scanner_checksum":"abc123","diagnostic":"process_metadata_partial"},"listener_observations":[{"family":"ipv4","bind_scope":"loopback","remote_port":8080,"socket_identities":["socket:one"],"process_chains":[{"processes":[{"pid":42,"executable":"/usr/bin/python3","working_directory":"/workspace","arguments":["python3","app.py"]}]}]}],"forwards":[{"id":"manual:operation-1","kind":"manual","remote_port":8080,"remote_family":"ipv4","allocated_local_port":8081,"local_families":["ipv4","ipv6"]}]}]}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeCompletesPipelinedHelloBeforeManagerRequest(t *testing.T) {
	session := newTestSession(t)
	hello := `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["unused"]}}`
	snapshot := `{"jsonrpc":"2.0","id":"2","method":"manager.snapshot","params":{"scope":{"kind":"all"}}}`
	writeDone := make(chan error, 1)
	go func() {
		_, err := session.client.Write([]byte(hello + "\n" + snapshot + "\n"))
		writeDone <- err
	}()

	helloResponse, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read pipelined hello response: %v", err)
	}
	wantHello := `{"jsonrpc":"2.0","id":"1","result":{"protocol":{"major":1,"minor":0},"capabilities":[],"max_frame_bytes":1048576}}`
	assertJSONEqual(t, helloResponse, []byte(wantHello))
	snapshotResponse, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read pipelined snapshot response: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write pipelined requests: %v", err)
	}
	wantSnapshot := `{"jsonrpc":"2.0","id":"2","result":{"snapshot":{"revision":0,"hosts":[]}}}`
	assertJSONEqual(t, snapshotResponse, []byte(wantSnapshot))
}

func TestServeRequiresHelloBeforeManagerMethods(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"manager.snapshot","params":{"scope":{"kind":"all"}}}`)
	want := `{"jsonrpc":"2.0","id":"1","error":{"code":-32001,"message":"system.hello is required before manager methods","data":{"kind":"hello_required","retryable":false}}}`
	assertJSONEqual(t, response, []byte(want))
	assertConnectionClosed(t, session)
}

func TestServeReturnsStableInvalidParameterError(t *testing.T) {
	session := newTestSession(t)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.snapshot","params":{"scope":{"kind":42}}}`)
	want := `{"jsonrpc":"2.0","id":"2","error":{"code":-32602,"message":"invalid parameters","data":{"kind":"invalid_parameters","retryable":false}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeRejectsUnknownSnapshotScope(t *testing.T) {
	session := newTestSession(t)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"2","method":"manager.snapshot","params":{"scope":{"kind":"unknown"}}}`)
	want := `{"jsonrpc":"2.0","id":"2","error":{"code":-32602,"message":"invalid parameters","data":{"kind":"invalid_scope","retryable":false}}}`
	assertJSONEqual(t, response, []byte(want))
}

func TestServeRejectsIncompatibleProtocolAndCloses(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":2,"minor":0},"capabilities":[]}}`)
	want := `{"jsonrpc":"2.0","id":"1","error":{"code":-32002,"message":"incompatible protocol major","data":{"kind":"incompatible_protocol","retryable":false,"supported":{"major":1,"minor":0}}}}`
	assertJSONEqual(t, response, []byte(want))
	assertConnectionClosed(t, session)
}

func TestServeClosesWhenHandshakeResponseExceedsFrameLimit(t *testing.T) {
	session := newTestSession(t)
	id := strings.Repeat("a", (1<<20)-180)
	request := fmt.Sprintf(`{"jsonrpc":"2.0","id":"%s","method":"system.hello","params":{"protocol":{"major":2,"minor":0},"capabilities":[]}}`, id)
	if len(request) > 1<<20 {
		t.Fatalf("test request is too large: %d", len(request))
	}
	if _, err := session.client.Write(append([]byte(request), '\n')); err != nil {
		t.Fatalf("write maximum-size hello: %v", err)
	}
	assertConnectionClosed(t, session)
	if err := session.wait(); err != nil {
		t.Fatalf("serve oversized handshake response: %v", err)
	}
}

func TestServeClosesWhenPostHandshakeResponseExceedsFrameLimit(t *testing.T) {
	session := newTestSession(t)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
	prefix := `{"jsonrpc":"2.0","id":"2","method":"`
	suffix := `"}`
	method := strings.Repeat("m", (1<<20)-len(prefix)-len(suffix))
	request := prefix + method + suffix
	if len(request) != 1<<20 {
		t.Fatalf("test request size = %d, want %d", len(request), 1<<20)
	}
	if _, err := session.client.Write(append([]byte(request), '\n')); err != nil {
		t.Fatalf("write maximum-size method: %v", err)
	}
	assertConnectionClosed(t, session)
	if err := session.wait(); err != nil {
		t.Fatalf("serve oversized method response: %v", err)
	}
}

func TestServeRejectsInvalidUTF8AndCloses(t *testing.T) {
	session := newTestSession(t)
	request := append([]byte(`{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":["`), 0xff)
	request = append(request, []byte(`"]}}`)...)
	request = append(request, '\n')
	if _, err := session.client.Write(request); err != nil {
		t.Fatalf("write invalid UTF-8 frame: %v", err)
	}
	response, err := session.reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read invalid UTF-8 response: %v", err)
	}
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"frame is not valid UTF-8"}}`
	assertJSONEqual(t, response, []byte(want))
	assertConnectionClosed(t, session)
}

func TestServeBoundsPendingCallsAndCancelsCleanly(t *testing.T) {
	manager := &blockingManager{started: make(chan struct{}, 8)}
	session := newTestSessionWithManager(t, manager)
	session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)

	var requests bytes.Buffer
	padding := strings.Repeat("x", 16<<10)
	for id := 2; id < 67; id++ {
		fmt.Fprintf(&requests, `{"jsonrpc":"2.0","id":"%d","method":"manager.snapshot","params":{"scope":{"kind":"all","padding":"%s"}}}`, id, padding)
		requests.WriteByte('\n')
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := session.client.Write(requests.Bytes())
		writeDone <- err
	}()
	for range 8 {
		select {
		case <-manager.started:
		case <-time.After(time.Second):
			t.Fatal("fewer than eight Manager.Snapshot handlers started")
		}
	}
	select {
	case <-manager.started:
		t.Fatal("more than eight Manager.Snapshot handlers started concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-writeDone:
		t.Fatalf("more than 64 pending requests were admitted: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	session.cancel()
	if err := session.wait(); err != nil {
		t.Fatalf("cancel saturated session: %v", err)
	}
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("blocked request writer did not stop after cancellation")
	}
}

func TestServeRejectsUnnegotiatedNotificationAndCloses(t *testing.T) {
	for _, request := range []string{
		`{"jsonrpc":"2.0","method":"manager.snapshot","params":{"scope":{"kind":"all"}}}`,
		`{"jsonrpc":"2.0","id":null,"method":"manager.snapshot","params":{"scope":{"kind":"all"}}}`,
	} {
		session := newTestSession(t)
		session.exchange(t, `{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}`)
		response := session.exchange(t, request)
		want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"notifications are not negotiated"}}`
		assertJSONEqual(t, response, []byte(want))
		assertConnectionClosed(t, session)
	}
}

func TestServeRejectsBatchFramesAndCloses(t *testing.T) {
	session := newTestSession(t)
	response := session.exchange(t, `[{"jsonrpc":"2.0","id":"1","method":"system.hello","params":{"protocol":{"major":1,"minor":0},"capabilities":[]}}]`)
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"batch requests are not supported"}}`
	assertJSONEqual(t, response, []byte(want))
	assertConnectionClosed(t, session)
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

func assertConnectionClosed(t *testing.T, session *testSession) {
	t.Helper()
	if _, err := session.reader.ReadByte(); err == nil {
		t.Fatal("connection remained open")
	} else {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatalf("timed out waiting for connection closure: %v", err)
		}
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
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("response = %s, want %s", got, want)
	}
}
