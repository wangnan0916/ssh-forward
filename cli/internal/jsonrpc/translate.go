package jsonrpc

import (
	"encoding/json"
	"fmt"

	"ssh-forward/cli/internal/core"
)

// MarshalCommand encodes a Manager command in the wire shape — the mirror
// of the per-kind decoders: an ipc client sends exactly what the adapter's
// decode functions accept, so the wire contract has one definition per
// command kind.
func MarshalCommand(command core.Command) ([]byte, error) {
	switch command := command.(type) {
	case core.AddManualForward:
		return json.Marshal(addManualForwardParams{
			Kind:        "manual_forward.add",
			OperationID: string(command.CommandID),
			Host:        string(command.Host),
			RemotePort:  command.RemotePort,
			Family:      string(command.Family),
		})
	case core.RemoveForward:
		return json.Marshal(removeForwardParams{
			Kind:        "manual_forward.remove",
			OperationID: string(command.CommandID),
			ForwardID:   string(command.ForwardID),
		})
	case core.ApproveListener:
		return json.Marshal(listenerDecisionParams{
			Kind:        "policy.approve",
			OperationID: string(command.CommandID),
			Host:        string(command.Host),
			RemotePort:  command.RemotePort,
			Family:      string(command.Family),
		})
	case core.SuppressListener:
		return json.Marshal(listenerDecisionParams{
			Kind:        "policy.suppress",
			OperationID: string(command.CommandID),
			Host:        string(command.Host),
			RemotePort:  command.RemotePort,
			Family:      string(command.Family),
		})
	default:
		return nil, fmt.Errorf("unknown command kind %T", command)
	}
}

// UnmarshalSnapshot decodes a wire Snapshot back into the domain shape —
// the mirror of MarshalSnapshot, serving the ipc client's Snapshot and
// Watch paths.
func UnmarshalSnapshot(data []byte) (core.Snapshot, error) {
	var wire wireSnapshot
	if err := json.Unmarshal(data, &wire); err != nil {
		return core.Snapshot{}, err
	}
	return translateSnapshot(wire)
}

func translateSnapshot(wire wireSnapshot) (core.Snapshot, error) {
	snapshot := core.Snapshot{Revision: core.Revision(wire.Revision)}
	if wire.Host == nil {
		return snapshot, nil
	}
	host := wire.Host
	translated := core.HostSnapshot{
		Alias:                core.HostAlias(host.Alias),
		Connection:           core.ConnectionState(host.Connection),
		Discovery:            translateDiscovery(host.Discovery),
		ListenerObservations: make([]core.ListenerObservation, 0, len(host.ListenerObservations)),
		ListenerLifetimes:    make([]core.ListenerLifetimeSnapshot, 0, len(host.ListenerLifetimes)),
		AskListeners:         make([]core.ListenerAskSnapshot, 0, len(host.AskListeners)),
		Forwards:             make([]core.ForwardSnapshot, 0, len(host.Forwards)),
	}
	for _, observation := range host.ListenerObservations {
		translated.ListenerObservations = append(translated.ListenerObservations, translateObservation(observation))
	}
	for _, lifetime := range host.ListenerLifetimes {
		translated.ListenerLifetimes = append(translated.ListenerLifetimes, core.ListenerLifetimeSnapshot{
			Family:       core.AddressFamily(lifetime.Family),
			BindScope:    core.ListenerBindScope(lifetime.BindScope),
			RemotePort:   lifetime.RemotePort,
			Status:       core.LifetimeStatus(lifetime.Status),
			PostBaseline: lifetime.PostBaseline,
		})
	}
	for _, ask := range host.AskListeners {
		translated.AskListeners = append(translated.AskListeners, core.ListenerAskSnapshot{
			Family:     core.AddressFamily(ask.Family),
			BindScope:  core.ListenerBindScope(ask.BindScope),
			RemotePort: ask.RemotePort,
		})
	}
	for _, forward := range host.Forwards {
		families := make([]core.AddressFamily, 0, len(forward.LocalFamilies))
		for _, family := range forward.LocalFamilies {
			families = append(families, core.AddressFamily(family))
		}
		translated.Forwards = append(translated.Forwards, core.ForwardSnapshot{
			ID:                 core.ForwardID(forward.ID),
			Kind:               core.ForwardKind(forward.Kind),
			RemotePort:         forward.RemotePort,
			RemoteFamily:       core.AddressFamily(forward.RemoteFamily),
			AllocatedLocalPort: forward.AllocatedLocalPort,
			LocalFamilies:      families,
		})
	}
	snapshot.Host = &translated
	return snapshot, nil
}

func translateDiscovery(wire wireDiscovery) core.DiscoverySnapshot {
	return core.DiscoverySnapshot{
		State:               core.DiscoveryState(wire.State),
		Capability:          core.DiscoveryCapability{RemoteListeners: core.CapabilityAvailability(wire.Capability.RemoteListeners), SocketIdentity: core.CapabilityAvailability(wire.Capability.SocketIdentity), ProcessMetadata: core.CapabilityAvailability(wire.Capability.ProcessMetadata)},
		BaselineEstablished: wire.BaselineEstablished,
		ScannerVersion:      wire.ScannerVersion,
		ScannerChecksum:     wire.ScannerChecksum,
		Diagnostic:          wire.Diagnostic,
	}
}

func translateObservation(wire wireListenerObservation) core.ListenerObservation {
	chains := make([]core.ProcessChain, 0, len(wire.ProcessChains))
	for _, chain := range wire.ProcessChains {
		processes := make([]core.ProcessMetadata, 0, len(chain.Processes))
		for _, process := range chain.Processes {
			processes = append(processes, core.ProcessMetadata{
				PID:              process.PID,
				Executable:       process.Executable,
				WorkingDirectory: process.WorkingDirectory,
				Arguments:        process.Arguments,
			})
		}
		chains = append(chains, core.ProcessChain{Processes: processes})
	}
	identities := make([]core.SocketIdentity, 0, len(wire.SocketIdentities))
	for _, identity := range wire.SocketIdentities {
		identities = append(identities, core.SocketIdentity(identity))
	}
	return core.ListenerObservation{
		Family:           core.AddressFamily(wire.Family),
		BindScope:        core.ListenerBindScope(wire.BindScope),
		RemotePort:       wire.RemotePort,
		SocketIdentities: identities,
		Processes:        chains,
	}
}
