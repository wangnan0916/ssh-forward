package core

import (
	"cmp"
	"slices"
)

const (
	// MaxRetained* are the published-snapshot retention caps for Discovery
	// evidence. The scanner declares its own per-scan budgets in-band; core
	// accepts declarations within these caps (see validObservationBudget),
	// so a scanner may use fewer records than these defaults but never more.
	// The scanner's parser caps are asserted equal to these in the openssh
	// package tests, keeping the protocol defaults in one numeric family.
	MaxRetainedListenerObservations = 256
	MaxRetainedSocketIdentities     = 512
	MaxRetainedProcessRecords       = 512
	MaxRetainedProcessMetadataBytes = 128 << 10
)

type evidenceTruncation struct {
	listeners bool
	sockets   bool
	processes bool
}

func stoppedDiscovery() DiscoverySnapshot {
	return DiscoverySnapshot{
		State: DiscoveryStopped,
		Capability: DiscoveryCapability{
			RemoteListeners: CapabilityUnavailable,
			SocketIdentity:  CapabilityUnavailable,
			ProcessMetadata: CapabilityUnavailable,
		},
	}
}

func startingDiscovery() DiscoverySnapshot {
	discovery := stoppedDiscovery()
	discovery.State = DiscoveryStarting
	return discovery
}

func validDiscoveryCapability(capability DiscoveryCapability) bool {
	return validCapabilityAvailability(capability.RemoteListeners) &&
		validCapabilityAvailability(capability.SocketIdentity) &&
		validCapabilityAvailability(capability.ProcessMetadata)
}

func validCapabilityAvailability(capability CapabilityAvailability) bool {
	switch capability {
	case CapabilityUnavailable, CapabilityPartial, CapabilityFull:
		return true
	default:
		return false
	}
}

// validObservationBudget requires the adapter's declared evidence budget to be
// present and within core's retention caps, so every full scan the adapter
// promises fits within what core retains.
func validObservationBudget(budget ObservationBudget) bool {
	return budget.Listeners >= 1 && budget.Listeners <= MaxRetainedListenerObservations &&
		budget.Sockets >= 1 && budget.Sockets <= MaxRetainedSocketIdentities &&
		budget.ProcessRecords >= 1 && budget.ProcessRecords <= MaxRetainedProcessRecords &&
		budget.MetadataBytes >= 1 && budget.MetadataBytes <= MaxRetainedProcessMetadataBytes
}

func discoveryStateForCapability(capability DiscoveryCapability) DiscoveryState {
	if capability.RemoteListeners == CapabilityFull && capability.SocketIdentity == CapabilityFull && capability.ProcessMetadata == CapabilityFull {
		return DiscoveryHealthy
	}
	return DiscoveryDegraded
}

type remoteListenerKey struct {
	family AddressFamily
	scope  ListenerBindScope
	port   uint16
}

func mergeBoundedListenerObservations(retained, current []ListenerObservation) ([]ListenerObservation, evidenceTruncation) {
	retained, truncated := boundListenerObservations(canonicalListenerObservations(retained))
	merged := make(map[remoteListenerKey]ListenerObservation, MaxRetainedListenerObservations)
	for _, observation := range retained {
		merged[listenerKey(observation)] = observation
	}
	for _, observation := range canonicalListenerObservations(current) {
		key := listenerKey(observation)
		if previous, found := merged[key]; found {
			merged[key] = mergePartialListenerObservation(previous, observation)
		} else if len(merged) < MaxRetainedListenerObservations {
			merged[key] = observation
		} else {
			truncated.listeners = true
		}
	}
	observations := make([]ListenerObservation, 0, len(merged))
	for _, observation := range merged {
		observations = append(observations, observation)
	}
	bounded, additional := boundListenerObservations(canonicalListenerObservations(observations))
	truncated.listeners = truncated.listeners || additional.listeners
	truncated.sockets = truncated.sockets || additional.sockets
	truncated.processes = truncated.processes || additional.processes
	return bounded, truncated
}

func degradeTruncatedCapability(capability *DiscoveryCapability, truncated evidenceTruncation) {
	if truncated.listeners && capability.RemoteListeners != CapabilityUnavailable {
		capability.RemoteListeners = CapabilityPartial
	}
	if truncated.sockets && capability.SocketIdentity != CapabilityUnavailable {
		capability.SocketIdentity = CapabilityPartial
	}
	if truncated.processes && capability.ProcessMetadata != CapabilityUnavailable {
		capability.ProcessMetadata = CapabilityPartial
	}
}

func boundListenerObservations(observations []ListenerObservation) ([]ListenerObservation, evidenceTruncation) {
	bounded := make([]ListenerObservation, 0, min(len(observations), MaxRetainedListenerObservations))
	var truncated evidenceTruncation
	socketCount := 0
	processCount := 0
	metadataBytes := 0
	for _, observation := range observations {
		if len(bounded) == MaxRetainedListenerObservations {
			truncated.listeners = true
			break
		}
		item := ListenerObservation{
			Family:     observation.Family,
			BindScope:  observation.BindScope,
			RemotePort: observation.RemotePort,
		}
		availableSockets := MaxRetainedSocketIdentities - socketCount
		keptSockets := min(len(observation.SocketIdentities), availableSockets)
		item.SocketIdentities = slices.Clone(observation.SocketIdentities[:keptSockets])
		socketCount += keptSockets
		if keptSockets != len(observation.SocketIdentities) {
			truncated.sockets = true
		}
		for _, chain := range observation.Processes {
			boundedChain := ProcessChain{}
			for _, process := range chain.Processes {
				size := processMetadataSize(process)
				if processCount == MaxRetainedProcessRecords || metadataBytes+size > MaxRetainedProcessMetadataBytes {
					truncated.processes = true
					break
				}
				process.Arguments = slices.Clone(process.Arguments)
				boundedChain.Processes = append(boundedChain.Processes, process)
				processCount++
				metadataBytes += size
			}
			if len(boundedChain.Processes) != 0 {
				item.Processes = append(item.Processes, boundedChain)
			}
			if len(boundedChain.Processes) != len(chain.Processes) {
				truncated.processes = true
			}
		}
		bounded = append(bounded, item)
	}
	return bounded, truncated
}

func processMetadataSize(process ProcessMetadata) int {
	size := len(process.Executable) + len(process.WorkingDirectory)
	for _, argument := range process.Arguments {
		size += len(argument) + 1
	}
	return size
}

func mergePartialListenerObservation(retained, current ListenerObservation) ListenerObservation {
	merged := cloneListenerObservation(retained)
	identities := make(map[SocketIdentity]struct{}, len(merged.SocketIdentities)+len(current.SocketIdentities))
	for _, identity := range merged.SocketIdentities {
		identities[identity] = struct{}{}
	}
	for _, identity := range current.SocketIdentities {
		if _, found := identities[identity]; !found {
			merged.SocketIdentities = append(merged.SocketIdentities, identity)
			identities[identity] = struct{}{}
		}
	}
	chains := make(map[int]int, len(merged.Processes))
	for index, chain := range merged.Processes {
		chains[firstProcessPID(chain)] = index
	}
	for _, chain := range current.Processes {
		pid := firstProcessPID(chain)
		if index, found := chains[pid]; found {
			merged.Processes[index] = chain
		} else {
			chains[pid] = len(merged.Processes)
			merged.Processes = append(merged.Processes, chain)
		}
	}
	return merged
}

func listenerKey(observation ListenerObservation) remoteListenerKey {
	return remoteListenerKey{
		family: observation.Family,
		scope:  observation.BindScope,
		port:   observation.RemotePort,
	}
}

func canonicalListenerObservations(observations []ListenerObservation) []ListenerObservation {
	canonical := cloneListenerObservations(observations)
	for index := range canonical {
		slices.Sort(canonical[index].SocketIdentities)
		slices.SortFunc(canonical[index].Processes, func(left, right ProcessChain) int {
			return cmp.Compare(firstProcessPID(left), firstProcessPID(right))
		})
	}
	slices.SortFunc(canonical, func(left, right ListenerObservation) int {
		if order := cmp.Compare(left.Family, right.Family); order != 0 {
			return order
		}
		if order := cmp.Compare(left.BindScope, right.BindScope); order != 0 {
			return order
		}
		if order := cmp.Compare(left.RemotePort, right.RemotePort); order != 0 {
			return order
		}
		return cmp.Compare(firstSocketIdentity(left), firstSocketIdentity(right))
	})
	return canonical
}

func cloneListenerObservations(observations []ListenerObservation) []ListenerObservation {
	cloned := make([]ListenerObservation, len(observations))
	for index, observation := range observations {
		cloned[index] = cloneListenerObservation(observation)
	}
	return cloned
}

func cloneListenerObservation(observation ListenerObservation) ListenerObservation {
	cloned := ListenerObservation{
		Family:           observation.Family,
		BindScope:        observation.BindScope,
		RemotePort:       observation.RemotePort,
		SocketIdentities: slices.Clone(observation.SocketIdentities),
		Processes:        make([]ProcessChain, len(observation.Processes)),
	}
	for chainIndex, chain := range observation.Processes {
		cloned.Processes[chainIndex].Processes = make([]ProcessMetadata, len(chain.Processes))
		for processIndex, process := range chain.Processes {
			process.Arguments = slices.Clone(process.Arguments)
			cloned.Processes[chainIndex].Processes[processIndex] = process
		}
	}
	return cloned
}

func firstProcessPID(chain ProcessChain) int {
	if len(chain.Processes) == 0 {
		return 0
	}
	return chain.Processes[0].PID
}

func firstSocketIdentity(observation ListenerObservation) SocketIdentity {
	if len(observation.SocketIdentities) == 0 {
		return ""
	}
	return observation.SocketIdentities[0]
}
