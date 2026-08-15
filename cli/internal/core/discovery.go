package core

import (
	"cmp"
	"reflect"
	"slices"
	"unicode/utf8"
)

const (
	maxRetainedListenerObservations = 256
	maxRetainedSocketIdentities     = 512
	maxRetainedProcessRecords       = 512
	maxRetainedProcessMetadataBytes = 128 << 10
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

func (m *manager) applySessionFact(fact SessionFact) {
	switch fact := fact.(type) {
	case ObservationSet:
		m.applyObservationSet(fact)
	case DiscoveryChange:
		m.applyDiscoveryChange(fact)
	default:
		m.applyInvalidDiscoveryFact()
	}
}

func (m *manager) applyObservationSet(set ObservationSet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if set.Sequence == 0 || set.Sequence <= m.lastObservationSequence || !validDiscoveryCapability(set.Capability) {
		m.failDiscoveryLocked("invalid_session_fact")
		return
	}
	gapped := set.Sequence != m.lastObservationSequence+1
	m.lastObservationSequence = set.Sequence
	capability := set.Capability
	observations, truncated := boundListenerObservations(canonicalListenerObservations(set.Observations))
	degradeTruncatedCapability(&capability, truncated)
	complete := capability.RemoteListeners == CapabilityFull
	if !complete {
		observations, truncated = mergeBoundedListenerObservations(m.listenerObservations, observations)
		degradeTruncatedCapability(&capability, truncated)
	}
	discovery := DiscoverySnapshot{
		State:               discoveryStateForCapability(capability),
		Capability:          capability,
		BaselineEstablished: complete || m.discovery.BaselineEstablished,
		ScannerVersion:      set.ScannerVersion,
		ScannerChecksum:     set.ScannerChecksum,
	}
	if gapped {
		discovery.State = DiscoveryDegraded
		discovery.Diagnostic = "observation_resync"
	}
	if reflect.DeepEqual(m.discovery, discovery) && reflect.DeepEqual(m.listenerObservations, observations) {
		return
	}
	m.discovery = discovery
	m.listenerObservations = observations
	m.publishLocked()
}

func (m *manager) applyDiscoveryChange(change DiscoveryChange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if (change.State != DiscoveryDegraded && change.State != DiscoveryFailed) ||
		!validDiscoveryCapability(change.Capability) || len(change.Diagnostic) > 128 || !utf8.ValidString(change.Diagnostic) {
		m.failDiscoveryLocked("invalid_session_fact")
		return
	}
	discovery := m.discovery
	discovery.State = change.State
	discovery.Capability = change.Capability
	discovery.Diagnostic = change.Diagnostic
	if reflect.DeepEqual(m.discovery, discovery) {
		return
	}
	m.discovery = discovery
	m.publishLocked()
}

func (m *manager) applyInvalidDiscoveryFact() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.failDiscoveryLocked("invalid_session_fact")
	}
}

func (m *manager) failDiscoveryLocked(diagnostic string) {
	discovery := m.discovery
	discovery.State = DiscoveryFailed
	discovery.Diagnostic = diagnostic
	if reflect.DeepEqual(m.discovery, discovery) {
		return
	}
	m.discovery = discovery
	m.publishLocked()
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
	merged := make(map[remoteListenerKey]ListenerObservation, maxRetainedListenerObservations)
	for _, observation := range retained {
		merged[listenerKey(observation)] = observation
	}
	for _, observation := range canonicalListenerObservations(current) {
		key := listenerKey(observation)
		if previous, found := merged[key]; found {
			merged[key] = mergePartialListenerObservation(previous, observation)
		} else if len(merged) < maxRetainedListenerObservations {
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
	bounded := make([]ListenerObservation, 0, min(len(observations), maxRetainedListenerObservations))
	var truncated evidenceTruncation
	socketCount := 0
	processCount := 0
	metadataBytes := 0
	for _, observation := range observations {
		if len(bounded) == maxRetainedListenerObservations {
			truncated.listeners = true
			break
		}
		item := ListenerObservation{
			Family:     observation.Family,
			BindScope:  observation.BindScope,
			RemotePort: observation.RemotePort,
		}
		availableSockets := maxRetainedSocketIdentities - socketCount
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
				if processCount == maxRetainedProcessRecords || metadataBytes+size > maxRetainedProcessMetadataBytes {
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
