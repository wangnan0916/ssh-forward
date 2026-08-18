package jsonrpc

import (
	"encoding/json"

	"github.com/creachadair/jrpc2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// MarshalSnapshot encodes a Snapshot in the wire shape (the same shape
// manager.snapshot returns over JSON-RPC), so CLI --json output and the IPC
// protocol stay one contract for script and desktop clients.
func MarshalSnapshot(snapshot core.Snapshot) ([]byte, error) {
	return json.Marshal(marshalSnapshot(snapshot))
}

// UnmarshalSnapshot decodes a wire Snapshot back into the domain shape —
// the mirror of MarshalSnapshot.
func UnmarshalSnapshot(data []byte) (core.Snapshot, error) {
	var wire wireSnapshot
	if err := json.Unmarshal(data, &wire); err != nil {
		return core.Snapshot{}, err
	}
	return translateSnapshot(wire), nil
}

func parseSnapshotParams(request *jrpc2.Request) error {
	paramsText := request.ParamString()
	if paramsText == "" || paramsText == "{}" {
		return nil
	}
	var params snapshotParams
	if json.Unmarshal([]byte(paramsText), &params) != nil {
		return errInvalidParameters
	}
	if params.Scope.Kind == "" || params.Scope.Kind == "all" {
		return nil
	}
	return errInvalidScope
}

func marshalSnapshot(snapshot core.Snapshot) wireSnapshot {
	if snapshot.Host == nil {
		return wireSnapshot{Revision: uint64(snapshot.Revision)}
	}
	host := snapshot.Host
	forwards := make([]wireForward, len(host.Forwards))
	for forwardIndex, forward := range host.Forwards {
		forwards[forwardIndex] = marshalForward(forward)
	}
	observations := make([]wireListenerObservation, len(host.ListenerObservations))
	for observationIndex, observation := range host.ListenerObservations {
		observations[observationIndex] = marshalListenerObservation(observation)
	}
	conflicts := make([]wireLocalPortConflict, len(host.LocalPortConflicts))
	for index, conflict := range host.LocalPortConflicts {
		conflicts[index] = wireLocalPortConflict{
			RemotePort:   conflict.RemotePort,
			RemoteFamily: string(conflict.RemoteFamily),
			BindScope:    string(conflict.BindScope),
		}
	}
	return wireSnapshot{
		Revision: uint64(snapshot.Revision),
		Host: &wireHost{
			Alias:                string(host.Alias),
			Connection:           string(host.Connection),
			Discovery:            marshalDiscovery(host.Discovery),
			ListenerObservations: observations,
			Forwards:             forwards,
			LocalPortConflicts:   conflicts,
		},
	}
}

func marshalForward(forward core.ForwardSnapshot) wireForward {
	families := make([]string, len(forward.LocalFamilies))
	for index, family := range forward.LocalFamilies {
		families[index] = string(family)
	}
	return wireForward{
		ID:                 string(forward.ID),
		RemotePort:         forward.RemotePort,
		RemoteFamily:       string(forward.RemoteFamily),
		AllocatedLocalPort: forward.AllocatedLocalPort,
		LocalFamilies:      families,
	}
}

func marshalDiscovery(discovery core.DiscoverySnapshot) wireDiscovery {
	return wireDiscovery{
		State: string(discovery.State),
		Capability: wireDiscoveryCapability{
			RemoteListeners: string(discovery.Capability.RemoteListeners),
			SocketIdentity:  string(discovery.Capability.SocketIdentity),
			ProcessMetadata: string(discovery.Capability.ProcessMetadata),
		},
		BaselineEstablished: discovery.BaselineEstablished,
		ScannerVersion:      discovery.ScannerVersion,
		ScannerChecksum:     discovery.ScannerChecksum,
		Diagnostic:          discovery.Diagnostic,
	}
}

func marshalListenerObservation(observation core.ListenerObservation) wireListenerObservation {
	identities := make([]string, len(observation.SocketIdentities))
	for index, identity := range observation.SocketIdentities {
		identities[index] = string(identity)
	}
	chains := make([]wireProcessChain, len(observation.Processes))
	for chainIndex, chain := range observation.Processes {
		processes := make([]wireProcessMetadata, len(chain.Processes))
		for processIndex, process := range chain.Processes {
			arguments := make([]string, len(process.Arguments))
			copy(arguments, process.Arguments)
			processes[processIndex] = wireProcessMetadata{
				PID:              process.PID,
				Executable:       process.Executable,
				WorkingDirectory: process.WorkingDirectory,
				Arguments:        arguments,
			}
		}
		chains[chainIndex] = wireProcessChain{Processes: processes}
	}
	return wireListenerObservation{
		Family:           string(observation.Family),
		BindScope:        string(observation.BindScope),
		RemotePort:       observation.RemotePort,
		SocketIdentities: identities,
		ProcessChains:    chains,
	}
}

func translateSnapshot(wire wireSnapshot) core.Snapshot {
	snapshot := core.Snapshot{Revision: core.Revision(wire.Revision)}
	if wire.Host == nil {
		return snapshot
	}
	host := wire.Host
	translated := core.HostSnapshot{
		Alias:                core.HostAlias(host.Alias),
		Connection:           core.ConnectionState(host.Connection),
		Discovery:            translateDiscovery(host.Discovery),
		ListenerObservations: make([]core.ListenerObservation, 0, len(host.ListenerObservations)),
		Forwards:             make([]core.ForwardSnapshot, 0, len(host.Forwards)),
	}
	for _, observation := range host.ListenerObservations {
		translated.ListenerObservations = append(translated.ListenerObservations, translateObservation(observation))
	}
	for _, forward := range host.Forwards {
		translated.Forwards = append(translated.Forwards, translateForward(forward))
	}
	if len(host.LocalPortConflicts) != 0 {
		translated.LocalPortConflicts = make([]core.LocalPortConflict, 0, len(host.LocalPortConflicts))
		for _, conflict := range host.LocalPortConflicts {
			translated.LocalPortConflicts = append(translated.LocalPortConflicts, core.LocalPortConflict{
				RemotePort:   conflict.RemotePort,
				RemoteFamily: core.AddressFamily(conflict.RemoteFamily),
				BindScope:    core.ListenerBindScope(conflict.BindScope),
			})
		}
	}
	snapshot.Host = &translated
	return snapshot
}

func translateDiscovery(wire wireDiscovery) core.DiscoverySnapshot {
	return core.DiscoverySnapshot{
		State: core.DiscoveryState(wire.State),
		Capability: core.DiscoveryCapability{
			RemoteListeners: core.CapabilityAvailability(wire.Capability.RemoteListeners),
			SocketIdentity:  core.CapabilityAvailability(wire.Capability.SocketIdentity),
			ProcessMetadata: core.CapabilityAvailability(wire.Capability.ProcessMetadata),
		},
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

func translateForward(wire wireForward) core.ForwardSnapshot {
	families := make([]core.AddressFamily, 0, len(wire.LocalFamilies))
	for _, family := range wire.LocalFamilies {
		families = append(families, core.AddressFamily(family))
	}
	return core.ForwardSnapshot{
		ID:                 core.ForwardID(wire.ID),
		RemotePort:         wire.RemotePort,
		RemoteFamily:       core.AddressFamily(wire.RemoteFamily),
		AllocatedLocalPort: wire.AllocatedLocalPort,
		LocalFamilies:      families,
	}
}
