package jsonrpc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"ssh-forward/cli/internal/core"
)

// The ipc client round-trips through the same wire contract the golden
// fixtures pin: MarshalCommand must produce exactly what the per-kind
// decoders accept, and UnmarshalSnapshot must invert MarshalSnapshot.

func TestMarshalCommandRoundTripsThroughDecoder(t *testing.T) {
	commands := []core.Command{
		core.AddManualForward{CommandID: "op-1", Host: "dev", RemotePort: 8080, Family: core.FamilyAuto},
		core.RemoveForward{CommandID: "op-2", ForwardID: "manual:op-1"},
		core.ApproveListener{CommandID: "op-3", Host: "dev", RemotePort: 9090},
		core.SuppressListener{CommandID: "op-4", Host: "dev", RemotePort: 9090, Family: core.FamilyIPv6},
	}
	decode := map[string]func([]byte) core.Command{
		"manual_forward.add":    decodeAddManualForward,
		"manual_forward.remove": decodeRemoveForward,
		"policy.approve": func(data []byte) core.Command {
			return decodeDecision(data, func(d listenerDecisionParams) core.Command {
				return core.ApproveListener{CommandID: core.CommandID(d.OperationID), Host: core.HostAlias(d.Host), RemotePort: d.RemotePort, Family: core.AddressFamily(d.Family)}
			})
		},
		"policy.suppress": func(data []byte) core.Command {
			return decodeDecision(data, func(d listenerDecisionParams) core.Command {
				return core.SuppressListener{CommandID: core.CommandID(d.OperationID), Host: core.HostAlias(d.Host), RemotePort: d.RemotePort, Family: core.AddressFamily(d.Family)}
			})
		},
	}
	for _, command := range commands {
		encoded, err := MarshalCommand(command)
		if err != nil {
			t.Fatalf("MarshalCommand(%T): %v", command, err)
		}
		var header commandHeader
		if err := json.Unmarshal(encoded, &header); err != nil {
			t.Fatalf("MarshalCommand(%T) is not a valid wire command: %v", command, err)
		}
		decoded := decode[header.Kind](encoded)
		if decoded == nil || !reflect.DeepEqual(decoded, command) {
			t.Fatalf("decoded %T = %#v, want %#v (encoded %s)", command, decoded, command, encoded)
		}
	}
}

func TestMarshalCommandRejectsUnknownKind(t *testing.T) {
	if _, err := MarshalCommand((*core.AddManualForward)(nil)); err == nil {
		t.Fatal("MarshalCommand on an unknown command succeeded")
	}
}

func TestMarshalCommandMatchesWireShape(t *testing.T) {
	encoded, err := MarshalCommand(core.AddManualForward{CommandID: "op-1", Host: "dev", RemotePort: 38080, Family: core.FamilyIPv4})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"manual_forward.add","operation_id":"op-1","host":"dev","remote_port":38080,"family":"ipv4"}`
	if !bytes.Equal(encoded, []byte(want)) {
		t.Fatalf("MarshalCommand = %s, want %s", encoded, want)
	}
}

func TestUnmarshalSnapshotInvertsMarshal(t *testing.T) {
	want := core.Snapshot{
		Revision: 9,
		Host: &core.HostSnapshot{
			Alias:      core.HostAlias("development"),
			Connection: core.ConnectionConnected,
			Discovery:  core.DiscoverySnapshot{State: core.DiscoveryHealthy, Capability: core.DiscoveryCapability{RemoteListeners: "full", SocketIdentity: "full", ProcessMetadata: "full"}, BaselineEstablished: true, ScannerVersion: 1, ScannerChecksum: "abc"},
			ListenerObservations: []core.ListenerObservation{{
				Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 8080,
				SocketIdentities: []core.SocketIdentity{"socket:one"},
				Processes:        []core.ProcessChain{{Processes: []core.ProcessMetadata{{PID: 42, Executable: "/usr/bin/python3", WorkingDirectory: "/workspace", Arguments: []string{"python3", "app.py"}}}}},
			}},
			ListenerLifetimes: []core.ListenerLifetimeSnapshot{{Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 8080, Status: core.LifetimeContinuous, PostBaseline: true}},
			AskListeners:      []core.ListenerAskSnapshot{{Family: core.FamilyIPv6, BindScope: core.BindLoopback, RemotePort: 9090}},
			Forwards: []core.ForwardSnapshot{{
				ID: core.ForwardID("managed:ipv4:loopback:8080"), Kind: core.ForwardManaged,
				RemotePort: 8080, RemoteFamily: core.FamilyIPv4, AllocatedLocalPort: 8081,
				LocalFamilies: []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
			}},
		},
	}
	encoded, err := MarshalSnapshot(want)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	got, err := UnmarshalSnapshot(encoded)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestUnmarshalSnapshotEmptyHost(t *testing.T) {
	got, err := UnmarshalSnapshot([]byte(`{"revision":0}`))
	if err != nil {
		t.Fatalf("UnmarshalSnapshot: %v", err)
	}
	if got.Revision != 0 || got.Host != nil {
		t.Fatalf("empty snapshot = %#v, want no host", got)
	}
}
