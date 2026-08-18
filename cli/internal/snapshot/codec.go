package snapshot

import (
	"encoding/json"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// Wire is the Snapshot shape on the JSON-RPC wire and in CLI --json
// output. One codec so script and desktop clients share one contract.
type Wire struct {
	Revision uint64 `json:"revision"`
	Host     *host  `json:"host,omitempty"`
}

type host struct {
	Alias                string                `json:"alias"`
	Connection           string                `json:"connection"`
	ConnectionDiagnostic string                `json:"connection_diagnostic,omitempty"`
	Discovery            discovery             `json:"discovery"`
	ListenerObservations []listenerObservation `json:"listener_observations"`
	Forwards             []forward             `json:"forwards"`
	LocalPortConflicts   []localPortConflict   `json:"local_port_conflicts,omitempty"`
	PolicyDiagnostic     string                `json:"policy_diagnostic,omitempty"`
}

type localPortConflict struct {
	RemotePort   uint16 `json:"remote_port"`
	RemoteFamily string `json:"remote_family"`
	BindScope    string `json:"bind_scope"`
}

type forward struct {
	ID                 string   `json:"id"`
	RemotePort         uint16   `json:"remote_port"`
	RemoteFamily       string   `json:"remote_family"`
	AllocatedLocalPort uint16   `json:"allocated_local_port"`
	LocalFamilies      []string `json:"local_families"`
}

type discovery struct {
	State               string              `json:"state"`
	Capability          discoveryCapability `json:"capability"`
	BaselineEstablished bool                `json:"baseline_established"`
	ScannerVersion      int                 `json:"scanner_version"`
	ScannerChecksum     string              `json:"scanner_checksum"`
	Diagnostic          string              `json:"diagnostic"`
}

type discoveryCapability struct {
	RemoteListeners string `json:"remote_listeners"`
	SocketIdentity  string `json:"socket_identity"`
	ProcessMetadata string `json:"process_metadata"`
}

type listenerObservation struct {
	Family           string         `json:"family"`
	BindScope        string         `json:"bind_scope"`
	RemotePort       uint16         `json:"remote_port"`
	SocketIdentities []string       `json:"socket_identities"`
	ProcessChains    []processChain `json:"process_chains"`
}

type processChain struct {
	Processes []processMetadata `json:"processes"`
}

type processMetadata struct {
	PID              int      `json:"pid"`
	Executable       string   `json:"executable"`
	WorkingDirectory string   `json:"working_directory"`
	Arguments        []string `json:"arguments"`
}

// Marshal encodes a Snapshot in the wire shape.
func Marshal(s core.Snapshot) ([]byte, error) {
	return json.Marshal(Encode(s))
}

// Unmarshal decodes a wire Snapshot back into the domain shape.
func Unmarshal(data []byte) (core.Snapshot, error) {
	var wire Wire
	if err := json.Unmarshal(data, &wire); err != nil {
		return core.Snapshot{}, err
	}
	return Decode(wire), nil
}

// Encode is the same mapping Marshal uses, returned as a value so the
// JSON-RPC adapter can embed it in a result without a second marshal.
func Encode(s core.Snapshot) Wire {
	if s.Host == nil {
		return Wire{Revision: uint64(s.Revision)}
	}
	hostSnap := s.Host
	forwards := make([]forward, len(hostSnap.Forwards))
	for i, item := range hostSnap.Forwards {
		forwards[i] = encodeForward(item)
	}
	observations := make([]listenerObservation, len(hostSnap.ListenerObservations))
	for i, item := range hostSnap.ListenerObservations {
		observations[i] = encodeObservation(item)
	}
	conflicts := make([]localPortConflict, len(hostSnap.LocalPortConflicts))
	for i, item := range hostSnap.LocalPortConflicts {
		conflicts[i] = localPortConflict{
			RemotePort:   item.RemotePort,
			RemoteFamily: string(item.RemoteFamily),
			BindScope:    string(item.BindScope),
		}
	}
	return Wire{
		Revision: uint64(s.Revision),
		Host: &host{
			Alias:                string(hostSnap.Alias),
			Connection:           string(hostSnap.Connection),
			ConnectionDiagnostic: hostSnap.ConnectionDiagnostic,
			Discovery:            encodeDiscovery(hostSnap.Discovery),
			ListenerObservations: observations,
			Forwards:             forwards,
			LocalPortConflicts:   conflicts,
			PolicyDiagnostic:     hostSnap.PolicyDiagnostic,
		},
	}
}

func encodeForward(item core.ForwardSnapshot) forward {
	families := make([]string, len(item.LocalFamilies))
	for i, family := range item.LocalFamilies {
		families[i] = string(family)
	}
	return forward{
		ID:                 string(item.ID),
		RemotePort:         item.RemotePort,
		RemoteFamily:       string(item.RemoteFamily),
		AllocatedLocalPort: item.AllocatedLocalPort,
		LocalFamilies:      families,
	}
}

func encodeDiscovery(item core.DiscoverySnapshot) discovery {
	return discovery{
		State: string(item.State),
		Capability: discoveryCapability{
			RemoteListeners: string(item.Capability.RemoteListeners),
			SocketIdentity:  string(item.Capability.SocketIdentity),
			ProcessMetadata: string(item.Capability.ProcessMetadata),
		},
		BaselineEstablished: item.BaselineEstablished,
		ScannerVersion:      item.ScannerVersion,
		ScannerChecksum:     item.ScannerChecksum,
		Diagnostic:          item.Diagnostic,
	}
}

func encodeObservation(item core.ListenerObservation) listenerObservation {
	identities := make([]string, len(item.SocketIdentities))
	for i, identity := range item.SocketIdentities {
		identities[i] = string(identity)
	}
	chains := make([]processChain, len(item.Processes))
	for i, chain := range item.Processes {
		processes := make([]processMetadata, len(chain.Processes))
		for j, process := range chain.Processes {
			arguments := make([]string, len(process.Arguments))
			copy(arguments, process.Arguments)
			processes[j] = processMetadata{
				PID:              process.PID,
				Executable:       process.Executable,
				WorkingDirectory: process.WorkingDirectory,
				Arguments:        arguments,
			}
		}
		chains[i] = processChain{Processes: processes}
	}
	return listenerObservation{
		Family:           string(item.Family),
		BindScope:        string(item.BindScope),
		RemotePort:       item.RemotePort,
		SocketIdentities: identities,
		ProcessChains:    chains,
	}
}

// Decode is the inverse of Encode.
func Decode(wire Wire) core.Snapshot {
	s := core.Snapshot{Revision: core.Revision(wire.Revision)}
	if wire.Host == nil {
		return s
	}
	h := wire.Host
	observations := make([]core.ListenerObservation, len(h.ListenerObservations))
	for i, observation := range h.ListenerObservations {
		observations[i] = decodeObservation(observation)
	}
	forwards := make([]core.ForwardSnapshot, len(h.Forwards))
	for i, item := range h.Forwards {
		forwards[i] = decodeForward(item)
	}
	translated := core.HostSnapshot{
		Alias:                core.HostAlias(h.Alias),
		Connection:           core.ConnectionState(h.Connection),
		ConnectionDiagnostic: h.ConnectionDiagnostic,
		Discovery:            decodeDiscovery(h.Discovery),
		ListenerObservations: observations,
		Forwards:             forwards,
		PolicyDiagnostic:     h.PolicyDiagnostic,
	}
	if len(h.LocalPortConflicts) != 0 {
		conflicts := make([]core.LocalPortConflict, len(h.LocalPortConflicts))
		for i, conflict := range h.LocalPortConflicts {
			conflicts[i] = core.LocalPortConflict{
				RemotePort:   conflict.RemotePort,
				RemoteFamily: core.AddressFamily(conflict.RemoteFamily),
				BindScope:    core.ListenerBindScope(conflict.BindScope),
			}
		}
		translated.LocalPortConflicts = conflicts
	}
	s.Host = &translated
	return s
}

func decodeDiscovery(wire discovery) core.DiscoverySnapshot {
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

func decodeObservation(wire listenerObservation) core.ListenerObservation {
	chains := make([]core.ProcessChain, len(wire.ProcessChains))
	for i, chain := range wire.ProcessChains {
		processes := make([]core.ProcessMetadata, len(chain.Processes))
		for j, process := range chain.Processes {
			processes[j] = core.ProcessMetadata{
				PID:              process.PID,
				Executable:       process.Executable,
				WorkingDirectory: process.WorkingDirectory,
				Arguments:        process.Arguments,
			}
		}
		chains[i] = core.ProcessChain{Processes: processes}
	}
	identities := make([]core.SocketIdentity, len(wire.SocketIdentities))
	for i, identity := range wire.SocketIdentities {
		identities[i] = core.SocketIdentity(identity)
	}
	return core.ListenerObservation{
		Family:           core.AddressFamily(wire.Family),
		BindScope:        core.ListenerBindScope(wire.BindScope),
		RemotePort:       wire.RemotePort,
		SocketIdentities: identities,
		Processes:        chains,
	}
}

func decodeForward(wire forward) core.ForwardSnapshot {
	families := make([]core.AddressFamily, len(wire.LocalFamilies))
	for i, family := range wire.LocalFamilies {
		families[i] = core.AddressFamily(family)
	}
	return core.ForwardSnapshot{
		ID:                 core.ForwardID(wire.ID),
		RemotePort:         wire.RemotePort,
		RemoteFamily:       core.AddressFamily(wire.RemoteFamily),
		AllocatedLocalPort: wire.AllocatedLocalPort,
		LocalFamilies:      families,
	}
}
