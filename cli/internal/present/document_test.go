package present

import (
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func testHost() *core.HostSnapshot {
	return &core.HostSnapshot{
		Alias:      core.HostAlias("ubuntu"),
		Connection: core.ConnectionConnected,
		Discovery:  core.DiscoverySnapshot{State: core.DiscoveryHealthy, Diagnostic: ""},
		ListenerObservations: []core.ListenerObservation{
			{Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 8080},
			{Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 9090},
		},
		Forwards: []core.ForwardSnapshot{{
			RemotePort: 8080, AllocatedLocalPort: 8080,
		}},
		PolicyDiagnostic: "",
	}
}

func TestAddablePortsSkipsIgnored(t *testing.T) {
	host := testHost()
	ignore := []core.ForwardingPolicy{{
		ID: "deny-9090", Action: core.PolicyIgnore,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 9090, To: 9090}}},
	}}
	if got := NewDocument(host, ignore, true).Addable; len(got) != 0 {
		t.Fatalf("addable = %v, want empty (9090 ignored)", got)
	}
	if got := NewDocument(host, nil, true).Addable; len(got) != 1 || got[0] != 9090 {
		t.Fatalf("addable without policies = %v, want [9090]", got)
	}
}

func TestAddablePortsSkipsWildcard(t *testing.T) {
	host := &core.HostSnapshot{
		ListenerObservations: []core.ListenerObservation{
			{Family: core.FamilyIPv4, BindScope: core.BindWildcard, RemotePort: 22},
			{Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 7897},
		},
	}
	if got := NewDocument(host, nil, true).Addable; len(got) != 1 || got[0] != 7897 {
		t.Fatalf("addable = %v, want [7897] (wildcard 22 omitted)", got)
	}
}

func TestAddablePortsSkipsAutoForward(t *testing.T) {
	host := testHost()
	policies := []core.ForwardingPolicy{{
		ID: "port-9090", Action: core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 9090, To: 9090}}},
	}}
	if got := NewDocument(host, policies, true).Addable; len(got) != 0 {
		t.Fatalf("addable = %v, want empty (9090 already matched)", got)
	}
}

func TestNewDocumentUnreliableOmitsWaitingAndRemembered(t *testing.T) {
	host := testHost()
	host.PolicyDiagnostic = "policies_file_invalid"
	policies := []core.ForwardingPolicy{{
		ID: "port-9090", Action: core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 9090, To: 9090}}},
	}}
	doc := NewDocument(host, policies, false)
	if len(doc.Remembered) != 0 {
		t.Fatalf("remembered = %v, want empty", doc.Remembered)
	}
	if len(doc.Lists.Waiting) != 0 {
		t.Fatalf("waiting = %+v, want empty", doc.Lists.Waiting)
	}
	if len(doc.Lists.Available) != 1 || doc.Lists.Available[0].Port != 9090 || doc.Lists.Available[0].Reason != ReasonUnclassified {
		t.Fatalf("available = %+v, want 9090 unclassified", doc.Lists.Available)
	}
	if len(doc.Addable) != 0 {
		t.Fatalf("addable = %v, want empty when unreliable", doc.Addable)
	}
	if doc.Chrome.PolicyDiagnostic != "policies_file_invalid" {
		t.Fatalf("chrome policy diagnostic = %q", doc.Chrome.PolicyDiagnostic)
	}
}

func TestNewDocumentChromeWireCodes(t *testing.T) {
	host := testHost()
	host.ConnectionDiagnostic = "authentication_failed"
	host.Discovery = core.DiscoverySnapshot{State: core.DiscoveryDegraded, Diagnostic: "process_metadata_unavailable"}
	doc := NewDocument(host, nil, true)
	if doc.Chrome.ConnectionDiagnostic != "authentication_failed" || doc.Chrome.DiscoveryDiagnostic != "process_metadata_unavailable" {
		t.Fatalf("chrome = %+v", doc.Chrome)
	}
}

func TestNewDocumentRememberedOnce(t *testing.T) {
	host := testHost()
	policies := []core.ForwardingPolicy{{
		ID: "port-5173", Action: core.PolicyAutoForward,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 5173, To: 5173}}},
	}}
	doc := NewDocument(host, policies, true)
	if len(doc.Remembered) != 1 || doc.Remembered[0] != 5173 {
		t.Fatalf("remembered = %v, want [5173]", doc.Remembered)
	}
	if len(doc.Lists.Waiting) != 1 || doc.Lists.Waiting[0].Port != 5173 {
		t.Fatalf("waiting = %+v, want 5173", doc.Lists.Waiting)
	}
}
