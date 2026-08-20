package present

import "github.com/wangnan0916/ssh-forward/cli/internal/core"

// Chrome is the HostSnapshot wire fields an operator view shows above the
// lists. English copy stays in the CLI; the WebUI renders these codes raw.
type Chrome struct {
	Alias                string `json:"alias"`
	Connection           string `json:"connection"`
	Discovery            string `json:"discovery"`
	ConnectionDiagnostic string `json:"connection_diagnostic,omitempty"`
	DiscoveryDiagnostic  string `json:"discovery_diagnostic,omitempty"`
	PolicyDiagnostic     string `json:"policy_diagnostic,omitempty"`
}

// Document is the operator view: Attention / Active / Waiting / Available,
// Remembered Auto-forward ports, Addable ports, and Host chrome. Policy
// Evidence is on Available rows. Chrome.PolicyDiagnostic is the Manager
// Snapshot's file health; Lists/Remembered/Addable use this process's
// Effective Policies (reliable).
type Document struct {
	Chrome     Chrome
	Lists      Lists
	Remembered []uint16
	Addable    []uint16
}

// NewDocument groups one HostSnapshot for CLI human status and the WebUI.
// reliable is false when this process has no last-valid Forwarding Policies
// (a cold read of a corrupt file): Waiting, Remembered, and Addable stay
// empty, and Available rows are unclassified instead of unmatched.
func NewDocument(host *core.HostSnapshot, policies []core.ForwardingPolicy, reliable bool) Document {
	doc := Document{Chrome: chrome(host)}
	if !reliable {
		lists := fromSnapshot(host, nil, nil)
		for i := range lists.Available {
			lists.Available[i].Reason = ReasonUnclassified
			lists.Available[i].PolicyID = ""
		}
		doc.Lists = lists
		return doc
	}
	doc.Remembered = core.SimpleAutoForwardPorts(policies)
	doc.Lists = fromSnapshot(host, doc.Remembered, policies)
	doc.Addable = addablePorts(host, doc.Lists)
	return doc
}

func chrome(host *core.HostSnapshot) Chrome {
	if host == nil {
		return Chrome{}
	}
	return Chrome{
		Alias:                string(host.Alias),
		Connection:           string(host.Connection),
		Discovery:            string(host.Discovery.State),
		ConnectionDiagnostic: host.ConnectionDiagnostic,
		DiscoveryDiagnostic:  host.Discovery.Diagnostic,
		PolicyDiagnostic:     host.PolicyDiagnostic,
	}
}

func addablePorts(host *core.HostSnapshot, lists Lists) []uint16 {
	ports := make([]uint16, 0, len(lists.Available))
	seen := make(map[uint16]struct{})
	for _, row := range lists.Available {
		if row.Reason == ReasonIgnored || row.Reason == ReasonAutoForward {
			continue
		}
		if !loopbackPort(host, row.Port) {
			continue
		}
		if _, ok := seen[row.Port]; ok {
			continue
		}
		seen[row.Port] = struct{}{}
		ports = append(ports, row.Port)
	}
	return ports
}

func loopbackPort(host *core.HostSnapshot, port uint16) bool {
	if host == nil {
		return false
	}
	for _, observation := range host.ListenerObservations {
		if observation.RemotePort == port && observation.BindScope == core.BindLoopback {
			return true
		}
	}
	return false
}
