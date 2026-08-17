package jsonrpc

import (
	"encoding/json"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

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
		Forwards:             make([]core.ForwardSnapshot, 0, len(host.Forwards)),
	}
	for _, observation := range host.ListenerObservations {
		translated.ListenerObservations = append(translated.ListenerObservations, translateObservation(observation))
	}
	for _, forward := range host.Forwards {
		filtered, err := translateForward(forward)
		if err != nil {
			return core.Snapshot{}, err
		}
		translated.Forwards = append(translated.Forwards, filtered)
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

func translateForward(wire wireForward) (core.ForwardSnapshot, error) {
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
	}, nil
}
