package openssh

import (
	"bufio"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	maxScannerFrameBytes = 64 << 10
	maxProcessArguments  = 64
	maxProcessDepth      = 16
	maxProcessTextBytes  = 4096
)

var errInvalidScannerFrame = errors.New("invalid scanner frame")

type scannerParser struct {
	active         bool
	sequence       uint64
	lastSequence   uint64
	capability     core.DiscoveryCapability
	listeners      []scannerListener
	listenerKeys   map[string]struct{}
	inodeListeners map[string]string
	inodes         map[string]struct{}
	processes      map[string]map[int]map[int]core.ProcessMetadata
	processCount   int
	metadataSize   int
}

type scannerListener struct {
	family core.AddressFamily
	scope  core.ListenerBindScope
	port   uint16
	inode  string
}

func scanObservationFrames(reader io.Reader, emit func(core.SessionFact)) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxScannerFrameBytes)
	parser := &scannerParser{}
	invalidCount := 0
	failed := false
	discarding := false
	lastCapability := unavailableDiscoveryCapability()
	recordInvalidObservation := func() {
		invalidCount++
		state := core.DiscoveryDegraded
		if invalidCount >= 3 {
			state = core.DiscoveryFailed
			failed = true
		}
		emit(core.DiscoveryChange{
			State:      state,
			Capability: lastCapability,
			Reason:     core.ReasonObservationInvalid,
		})
	}
	for scanner.Scan() {
		if failed {
			continue
		}
		line := scanner.Text()
		if discarding {
			if isScannerBeginFrame(line) {
				discarding = false
			} else {
				if isObservationBegin(line) {
					recordInvalidObservation()
				}
				continue
			}
		}
		set, complete, err := parser.accept(line)
		if err != nil {
			parser.abort()
			discarding = true
			recordInvalidObservation()
			continue
		}
		if !complete {
			continue
		}
		lastCapability = set.Capability
		invalidCount = 0
		emit(set)
	}
	if scanner.Err() != nil && !failed {
		emit(core.DiscoveryChange{
			State:      core.DiscoveryFailed,
			Capability: lastCapability,
			Reason:     core.ReasonObservationLost,
		})
	}
	_, _ = io.Copy(io.Discard, reader)
}

func isScannerBeginFrame(line string) bool {
	return utf8.ValidString(line) && strings.HasPrefix(line, "SF1\tB\t")
}

func isObservationBegin(line string) bool {
	if !utf8.ValidString(line) {
		return false
	}
	fields := strings.SplitN(line, "\t", 3)
	return len(fields) >= 2 && fields[1] == "B"
}

func (p *scannerParser) accept(line string) (core.ObservationSet, bool, error) {
	if !utf8.ValidString(line) || len(line) == 0 {
		return core.ObservationSet{}, false, errInvalidScannerFrame
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 2 || fields[0] != "SF1" {
		return core.ObservationSet{}, false, errInvalidScannerFrame
	}
	switch fields[1] {
	case "B":
		return core.ObservationSet{}, false, p.begin(fields)
	case "L":
		return core.ObservationSet{}, false, p.listener(fields)
	case "P":
		return core.ObservationSet{}, false, p.process(fields)
	case "E":
		set, err := p.end(fields)
		if err != nil {
			return core.ObservationSet{}, false, err
		}
		return set, true, nil
	default:
		return core.ObservationSet{}, false, errInvalidScannerFrame
	}
}

func (p *scannerParser) begin(fields []string) error {
	if p.active || len(fields) != 5 {
		return errInvalidScannerFrame
	}
	sequence, err := parseUint(fields[2], 64)
	if err != nil || sequence == 0 || sequence <= p.lastSequence {
		return errInvalidScannerFrame
	}
	remoteListeners, err := parseCapability(fields[3])
	if err != nil {
		return err
	}
	processMetadata, err := parseCapability(fields[4])
	if err != nil {
		return err
	}
	p.active = true
	p.sequence = sequence
	p.capability = core.DiscoveryCapability{
		RemoteListeners: remoteListeners,
		ProcessMetadata: processMetadata,
	}
	p.listeners = nil
	p.listenerKeys = make(map[string]struct{})
	p.inodeListeners = make(map[string]string)
	p.inodes = make(map[string]struct{})
	p.processes = make(map[string]map[int]map[int]core.ProcessMetadata)
	p.processCount = 0
	p.metadataSize = 0
	return nil
}

func (p *scannerParser) listener(fields []string) error {
	if !p.active || len(fields) != 7 || fields[2] != strconv.FormatUint(p.sequence, 10) || len(p.listeners) >= core.MaxRetainedListenerObservations {
		return errInvalidScannerFrame
	}
	family := core.AddressFamily(fields[3])
	if family != core.FamilyIPv4 && family != core.FamilyIPv6 {
		return errInvalidScannerFrame
	}
	scope := core.ListenerBindScope(fields[4])
	if scope != core.BindLoopback && scope != core.BindWildcard {
		return errInvalidScannerFrame
	}
	port, err := parseUint(fields[5], 16)
	if err != nil || port == 0 {
		return errInvalidScannerFrame
	}
	inode, err := parseDecimalIdentity(fields[6])
	if err != nil {
		return err
	}
	endpointKey := strings.Join([]string{string(family), string(scope), strconv.FormatUint(port, 10)}, "\x00")
	key := endpointKey + "\x00" + inode
	if _, duplicate := p.listenerKeys[key]; duplicate {
		return errInvalidScannerFrame
	}
	if previous, found := p.inodeListeners[inode]; inode != "0" && found && previous != endpointKey {
		return errInvalidScannerFrame
	}
	p.listenerKeys[key] = struct{}{}
	if inode != "0" {
		p.inodeListeners[inode] = endpointKey
	}
	p.listeners = append(p.listeners, scannerListener{
		family: family,
		scope:  scope,
		port:   uint16(port),
		inode:  inode,
	})
	p.inodes[inode] = struct{}{}
	return nil
}

func (p *scannerParser) process(fields []string) error {
	if !p.active || len(fields) != 10 || fields[2] != strconv.FormatUint(p.sequence, 10) || p.processCount >= core.MaxRetainedProcessRecords {
		return errInvalidScannerFrame
	}
	inode, err := parseDecimalIdentity(fields[3])
	if err != nil || inode == "0" {
		return errInvalidScannerFrame
	}
	if _, found := p.inodes[inode]; !found {
		return errInvalidScannerFrame
	}
	owner, err := parsePositiveInt(fields[4])
	if err != nil {
		return err
	}
	depth, err := parseUint(fields[5], 8)
	if err != nil || depth >= maxProcessDepth {
		return errInvalidScannerFrame
	}
	pid, err := parsePositiveInt(fields[6])
	if err != nil {
		return err
	}
	executable, executableAvailable, err := decodeMetadataText(fields[7])
	if err != nil {
		return err
	}
	recordSize := (len(fields[7]) + len(fields[8]) + len(fields[9])) / 2
	if p.metadataSize+recordSize > core.MaxRetainedProcessMetadataBytes {
		return errInvalidScannerFrame
	}
	workingDirectory, workingDirectoryAvailable, err := decodeMetadataText(fields[8])
	if err != nil {
		return err
	}
	arguments, argumentsAvailable, err := decodeArguments(fields[9])
	if err != nil {
		return err
	}
	if !executableAvailable || !workingDirectoryAvailable || !argumentsAvailable {
		p.capability.ProcessMetadata = core.CapabilityPartial
	}
	p.metadataSize += recordSize
	owners := p.processes[inode]
	if owners == nil {
		owners = make(map[int]map[int]core.ProcessMetadata)
		p.processes[inode] = owners
	}
	chain := owners[owner]
	if chain == nil {
		chain = make(map[int]core.ProcessMetadata)
		owners[owner] = chain
	}
	if _, duplicate := chain[int(depth)]; duplicate {
		return errInvalidScannerFrame
	}
	chain[int(depth)] = core.ProcessMetadata{
		PID:              pid,
		Executable:       executable,
		WorkingDirectory: workingDirectory,
		Arguments:        arguments,
	}
	p.processCount++
	return nil
}

func (p *scannerParser) end(fields []string) (core.ObservationSet, error) {
	if !p.active || len(fields) != 3 || fields[2] != strconv.FormatUint(p.sequence, 10) {
		return core.ObservationSet{}, errInvalidScannerFrame
	}
	observations := make(map[string]*core.ListenerObservation)
	for _, listener := range p.listeners {
		key := strings.Join([]string{string(listener.family), string(listener.scope), strconv.Itoa(int(listener.port))}, "\x00")
		observation := observations[key]
		if observation == nil {
			observation = &core.ListenerObservation{
				Family:     listener.family,
				BindScope:  listener.scope,
				RemotePort: listener.port,
			}
			observations[key] = observation
		}
		observation.Processes = append(observation.Processes, p.processChains(listener.inode)...)
	}
	items := make([]core.ListenerObservation, 0, len(observations))
	for _, observation := range observations {
		items = append(items, *observation)
	}
	set := core.ObservationSet{
		Sequence:     p.sequence,
		Capability:   p.capability,
		Observations: items,
	}
	p.lastSequence = p.sequence
	p.abort()
	return set, nil
}

func (p *scannerParser) processChains(inode string) []core.ProcessChain {
	owners := p.processes[inode]
	if len(owners) == 0 && p.capability.ProcessMetadata == core.CapabilityFull {
		p.capability.ProcessMetadata = core.CapabilityPartial
	}
	chains := make([]core.ProcessChain, 0, len(owners))
	for _, records := range owners {
		if _, found := records[0]; !found {
			p.capability.ProcessMetadata = core.CapabilityPartial
			continue
		}
		chain := core.ProcessChain{Processes: make([]core.ProcessMetadata, 0, len(records))}
		for depth := 0; depth < maxProcessDepth; depth++ {
			process, found := records[depth]
			if !found {
				break
			}
			chain.Processes = append(chain.Processes, process)
		}
		if len(chain.Processes) != len(records) {
			p.capability.ProcessMetadata = core.CapabilityPartial
		}
		chains = append(chains, chain)
	}
	return chains
}

func (p *scannerParser) abort() {
	p.active = false
	p.sequence = 0
	p.capability = core.DiscoveryCapability{}
	p.listeners = nil
	p.listenerKeys = nil
	p.inodeListeners = nil
	p.inodes = nil
	p.processes = nil
	p.processCount = 0
	p.metadataSize = 0
}

func parseCapability(value string) (core.CapabilityAvailability, error) {
	capability := core.CapabilityAvailability(value)
	switch capability {
	case core.CapabilityUnavailable, core.CapabilityPartial, core.CapabilityFull:
		return capability, nil
	default:
		return "", errInvalidScannerFrame
	}
}

func unavailableDiscoveryCapability() core.DiscoveryCapability {
	return core.DiscoveryCapability{
		RemoteListeners: core.CapabilityUnavailable,
		ProcessMetadata: core.CapabilityUnavailable,
	}
}

func parseUint(value string, bits int) (uint64, error) {
	if value == "" || len(value) > 20 {
		return 0, errInvalidScannerFrame
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	if err != nil {
		return 0, errInvalidScannerFrame
	}
	return parsed, nil
}

func parsePositiveInt(value string) (int, error) {
	parsed, err := parseUint(value, 31)
	if err != nil || parsed == 0 {
		return 0, errInvalidScannerFrame
	}
	return int(parsed), nil
}

func parseDecimalIdentity(value string) (string, error) {
	parsed, err := parseUint(value, 64)
	if err != nil || value != strconv.FormatUint(parsed, 10) {
		return "", errInvalidScannerFrame
	}
	return value, nil
}

func decodeBoundedHex(value string, maximum int) ([]byte, error) {
	if len(value) > maximum*2 || len(value)%2 != 0 {
		return nil, errInvalidScannerFrame
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) > maximum {
		return nil, errInvalidScannerFrame
	}
	return decoded, nil
}

func decodeMetadataText(value string) (string, bool, error) {
	decoded, err := decodeBoundedHex(value, maxProcessTextBytes)
	if err != nil {
		return "", false, err
	}
	if len(decoded) == 0 || !utf8.Valid(decoded) || strings.IndexByte(string(decoded), 0) >= 0 {
		return "", false, nil
	}
	return string(decoded), true, nil
}

func decodeArguments(value string) ([]string, bool, error) {
	decoded, err := decodeBoundedHex(value, maxProcessTextBytes)
	if err != nil {
		return nil, false, err
	}
	if len(decoded) == 0 || !utf8.Valid(decoded) {
		return nil, false, nil
	}
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	complete := len(parts) <= maxProcessArguments
	if !complete {
		parts = parts[:maxProcessArguments]
	}
	return parts, complete, nil
}
