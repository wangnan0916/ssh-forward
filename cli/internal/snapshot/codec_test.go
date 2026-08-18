package snapshot

import (
	"reflect"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestUnmarshalInvertsMarshal(t *testing.T) {
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
			Forwards: []core.ForwardSnapshot{{
				ID:         core.ForwardID("managed:ipv4:loopback:8080"),
				RemotePort: 8080, RemoteFamily: core.FamilyIPv4, AllocatedLocalPort: 8081,
				LocalFamilies: []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
			}},
			LocalPortConflicts: []core.LocalPortConflict{{
				RemotePort: 3000, RemoteFamily: core.FamilyIPv4, BindScope: core.BindLoopback,
			}},
		},
	}
	encoded, err := Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Unmarshal(encoded)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestUnmarshalEmptyHost(t *testing.T) {
	got, err := Unmarshal([]byte(`{"revision":0}`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Revision != 0 || got.Host != nil {
		t.Fatalf("empty snapshot = %#v, want no host", got)
	}
}
