package present

import (
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestFromSnapshotSplitsFourLists(t *testing.T) {
	exe := "node"
	cwd := "/home/dev/app"
	host := &core.HostSnapshot{
		ListenerObservations: []core.ListenerObservation{
			{RemotePort: 8080, Family: core.FamilyIPv4, BindScope: core.BindLoopback, Processes: []core.ProcessChain{{Processes: []core.ProcessMetadata{{Executable: exe, WorkingDirectory: cwd}}}}},
			{RemotePort: 9090, Family: core.FamilyIPv4, BindScope: core.BindLoopback},
			{RemotePort: 3000, Family: core.FamilyIPv4, BindScope: core.BindLoopback},
		},
		Forwards: []core.ForwardSnapshot{{
			RemotePort: 8080, AllocatedLocalPort: 8080,
		}},
		LocalPortConflicts: []core.LocalPortConflict{{RemotePort: 3000}},
	}
	lists := FromSnapshot(host, []uint16{5173, 8080}, nil)
	if len(lists.Attention) != 1 || lists.Attention[0].Port != 3000 || lists.Attention[0].State != StateConflict {
		t.Fatalf("attention = %+v", lists.Attention)
	}
	if len(lists.Active) != 1 || lists.Active[0].Port != 8080 || lists.Active[0].Local != 8080 || lists.Active[0].Exe != exe {
		t.Fatalf("active = %+v", lists.Active)
	}
	if len(lists.Waiting) != 1 || lists.Waiting[0].Port != 5173 {
		t.Fatalf("waiting = %+v, want remembered 5173 only", lists.Waiting)
	}
	if len(lists.Available) != 1 || lists.Available[0].Port != 9090 || lists.Available[0].Reason != ReasonUnmatched {
		t.Fatalf("available = %+v, want 9090 unmatched (3000 is Attention)", lists.Available)
	}
}

func TestFromSnapshotPolicyEvidence(t *testing.T) {
	ignore := []core.ForwardingPolicy{{
		ID: "deny-9090", Action: core.PolicyIgnore,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 9090, To: 9090}}},
	}}
	host := &core.HostSnapshot{
		ListenerObservations: []core.ListenerObservation{{RemotePort: 9090}},
	}
	lists := FromSnapshot(host, nil, ignore)
	if len(lists.Available) != 1 || lists.Available[0].Reason != ReasonIgnored || lists.Available[0].PolicyID != "deny-9090" {
		t.Fatalf("ignored available = %+v", lists.Available)
	}

	auto := []core.ForwardingPolicy{{
		ID: "port-9090", Action: core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 9090, To: 9090}}},
	}}
	lists = FromSnapshot(host, nil, auto)
	if lists.Available[0].Reason != ReasonAutoForward {
		t.Fatalf("auto-forward reason = %q", lists.Available[0].Reason)
	}

	needsCwd := []core.ForwardingPolicy{{
		ID: "dir", Action: core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{WorkingDirectoryTree: strPtr("/home/dev")}},
	}}
	lists = FromSnapshot(host, nil, needsCwd)
	if lists.Available[0].Reason != ReasonMissingEvidence {
		t.Fatalf("missing evidence reason = %q", lists.Available[0].Reason)
	}
}

func TestFromSnapshotNilHost(t *testing.T) {
	lists := FromSnapshot(nil, []uint16{1}, nil)
	if len(lists.Attention)+len(lists.Active)+len(lists.Waiting)+len(lists.Available) != 0 {
		t.Fatalf("nil host lists = %+v", lists)
	}
}

func strPtr(value string) *string { return &value }
