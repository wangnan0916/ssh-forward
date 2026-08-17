package jsonrpc

import (
	"reflect"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

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
