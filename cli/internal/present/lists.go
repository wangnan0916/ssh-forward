package present

import (
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	StateConflict  = "conflict"
	StateActive    = "active"
	StateWaiting   = "waiting"
	StateAvailable = "available"

	ReasonUnmatched       = "unmatched"
	ReasonIgnored         = "ignored"
	ReasonAutoForward     = "auto_forward"
	ReasonMissingEvidence = "missing_evidence"
	ReasonUnclassified    = "unclassified"
)

// Row is one WebUI / human-status line: a Remote Listener, Forward, conflict,
// or remembered port with no current listener.
type Row struct {
	State    string `json:"state"`
	Port     uint16 `json:"port"`
	Local    uint16 `json:"local,omitempty"`
	Exe      string `json:"exe,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Reason   string `json:"reason,omitempty"`
	PolicyID string `json:"policy_id,omitempty"`
}

// Lists is the Attention / Active / Waiting / Available grouping from a
// HostSnapshot plus Remembered Auto-forward ports. Policy Evidence lives on
// Available rows; it is not added to the Manager Snapshot.
type Lists struct {
	Attention []Row `json:"attention"`
	Active    []Row `json:"active"`
	Waiting   []Row `json:"waiting"`
	Available []Row `json:"available"`
}

func fromSnapshot(host *core.HostSnapshot, remembered []uint16, policies []core.ForwardingPolicy) Lists {
	if host == nil {
		return Lists{
			Attention: []Row{},
			Active:    []Row{},
			Waiting:   []Row{},
			Available: []Row{},
		}
	}
	forwarded := make(map[uint16]struct{}, len(host.Forwards))
	for _, forward := range host.Forwards {
		forwarded[forward.RemotePort] = struct{}{}
	}
	conflicted := make(map[uint16]struct{}, len(host.LocalPortConflicts))
	for _, conflict := range host.LocalPortConflicts {
		conflicted[conflict.RemotePort] = struct{}{}
	}
	obsByPort := make(map[uint16]core.ListenerObservation, len(host.ListenerObservations))
	for _, observation := range host.ListenerObservations {
		obsByPort[observation.RemotePort] = observation
	}

	lists := Lists{
		Attention: make([]Row, 0, len(host.LocalPortConflicts)),
		Active:    make([]Row, 0, len(host.Forwards)),
		Waiting:   make([]Row, 0),
		Available: make([]Row, 0),
	}
	for _, conflict := range host.LocalPortConflicts {
		exe, cwd := processSummary(obsByPort[conflict.RemotePort])
		lists.Attention = append(lists.Attention, Row{
			State: StateConflict, Port: conflict.RemotePort, Exe: exe, Cwd: cwd,
		})
	}
	for _, forward := range host.Forwards {
		exe, cwd := processSummary(obsByPort[forward.RemotePort])
		lists.Active = append(lists.Active, Row{
			State: StateActive, Port: forward.RemotePort, Local: forward.AllocatedLocalPort, Exe: exe, Cwd: cwd,
		})
	}
	for _, port := range remembered {
		if _, ok := obsByPort[port]; ok {
			continue
		}
		lists.Waiting = append(lists.Waiting, Row{State: StateWaiting, Port: port})
	}
	for _, observation := range host.ListenerObservations {
		if _, ok := forwarded[observation.RemotePort]; ok {
			continue
		}
		if _, ok := conflicted[observation.RemotePort]; ok {
			continue
		}
		exe, cwd := processSummary(observation)
		reason, policyID := availableReason(observation, policies)
		lists.Available = append(lists.Available, Row{
			State: StateAvailable, Port: observation.RemotePort, Exe: exe, Cwd: cwd, Reason: reason, PolicyID: policyID,
		})
	}
	return lists
}

func processSummary(observation core.ListenerObservation) (exe, cwd string) {
	if len(observation.Processes) == 0 || len(observation.Processes[0].Processes) == 0 {
		return "", ""
	}
	proc := observation.Processes[0].Processes[0]
	return proc.Executable, proc.WorkingDirectory
}

func availableReason(observation core.ListenerObservation, policies []core.ForwardingPolicy) (string, string) {
	if len(policies) == 0 {
		return ReasonUnmatched, ""
	}
	verdict := core.EvaluatePolicies(policies, observation)
	switch verdict.Action {
	case core.PolicyIgnore:
		return ReasonIgnored, verdict.PolicyID
	case core.PolicyAutoForward:
		return ReasonAutoForward, verdict.PolicyID
	}
	if len(observation.Processes) == 0 && needsProcessEvidence(policies, observation.RemotePort) {
		return ReasonMissingEvidence, ""
	}
	return ReasonUnmatched, ""
}

func needsProcessEvidence(policies []core.ForwardingPolicy, port uint16) bool {
	for _, policy := range policies {
		for _, condition := range policy.Conditions {
			if condition.Executable == nil && condition.AncestorExecutable == nil && condition.WorkingDirectoryTree == nil {
				continue
			}
			if condition.RemotePorts != nil && (port < condition.RemotePorts.From || port > condition.RemotePorts.To) {
				continue
			}
			return true
		}
	}
	return false
}
