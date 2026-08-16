package openssh

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"ssh-forward/cli/internal/core"
)

const (
	maxScannerFrameBytes = 64 << 10
	maxProcessDepth      = 16
	maxProcessArguments  = 64
	maxProcessTextBytes  = 4096
	maxIdentityTextBytes = 256

	// MaxObservedSockets is the parser's upper bound for the socket budget a
	// scanner may declare. It deliberately stays a parser-local constant: the
	// script's socket_record_limit is a derived report value equal to
	// listener_limit, not the same limit, so deriving from it would narrow
	// the parser's tolerance for no benefit.
	MaxObservedSockets = 512
)

// MaxObserved* are the parser's frame caps: the largest per-scan budgets a
// scanner may declare. The three that the script actually enforces are
// derived from the embedded scanner.sh, which is the single declaration of
// the evidence budget family; core's retention caps remain the second copy,
// pinned by TestScannerScriptDeclaresParserDefaultBudgets.
var (
	MaxObservedListeners        = scannerBudget("listener_limit")
	MaxProcessRecords           = scannerBudget("process_record_limit")
	MaxObservationMetadataBytes = scannerBudget("metadata_bytes_limit")
)

// scannerBudget reads one budget declaration from the embedded scanner
// script, following derived declarations such as socket_record_limit and
// metadata_hex_limit back to their numeric source. A missing or malformed
// declaration is a package build error: the parser and the script it parses
// must agree at startup, not by convention.
func scannerBudget(name string) int {
	for _, line := range strings.Split(scannerScript, "\n") {
		if !strings.HasPrefix(line, name+"=") {
			continue
		}
		value := strings.TrimPrefix(line, name+"=")
		if number, err := strconv.Atoi(value); err == nil {
			return number
		}
		if strings.HasPrefix(value, "$") {
			return scannerBudget(strings.TrimPrefix(value, "$"))
		}
		panic("scanner.sh declares " + name + " with an unsupported expression: " + value)
	}
	panic("scanner.sh does not declare " + name)
}

var errInvalidScannerFrame = errors.New("invalid scanner frame")

type scannerParser struct {
	active         bool
	sequence       uint64
	lastSequence   uint64
	boot           string
	network        string
	capability     core.DiscoveryCapability
	budget         core.ObservationBudget
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
			Reason:     core.ReasonFrameInvalid,
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
		fact, complete, err := parser.accept(line)
		if err != nil {
			parser.abort()
			discarding = true
			recordInvalidObservation()
			continue
		}
		if !complete {
			continue
		}
		set := fact.(core.ObservationSet)
		lastCapability = set.Capability
		invalidCount = 0
		emit(set)
	}
	if scanner.Err() != nil && !failed {
		emit(core.DiscoveryChange{
			State:      core.DiscoveryFailed,
			Capability: lastCapability,
			Reason:     core.ReasonStreamFailed,
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

// accept consumes one wire frame line. The frame layout is the contract
// documented at the top of scanner.sh (the embedded script); this parser is
// its consuming side, and the two must be extended together.
func (p *scannerParser) accept(line string) (core.SessionFact, bool, error) {
	if !utf8.ValidString(line) || len(line) == 0 {
		return nil, false, errInvalidScannerFrame
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 2 || fields[0] != "SF1" {
		return nil, false, errInvalidScannerFrame
	}
	switch fields[1] {
	case "B":
		return nil, false, p.begin(fields)
	case "L":
		return nil, false, p.listener(fields)
	case "P":
		return nil, false, p.process(fields)
	case "E":
		set, err := p.end(fields)
		if err != nil {
			return nil, false, err
		}
		return set, true, nil
	default:
		return nil, false, errInvalidScannerFrame
	}
}

func (p *scannerParser) begin(fields []string) error {
	if p.active || len(fields) != 12 {
		return errInvalidScannerFrame
	}
	sequence, err := parseUint(fields[2], 64)
	// Cheap stream-local filter: a non-increasing sequence cannot be a
	// legitimate observation. The actor's re-validation gate (applyObservationSet)
	// is the authority; this only avoids passing bad frames up the stream.
	if err != nil || sequence == 0 || sequence <= p.lastSequence {
		return errInvalidScannerFrame
	}
	boot, err := decodeText(fields[3], maxIdentityTextBytes, false)
	if err != nil {
		return errInvalidScannerFrame
	}
	network, err := decodeText(fields[4], maxIdentityTextBytes, false)
	if err != nil {
		return errInvalidScannerFrame
	}
	remoteListeners, err := parseCapability(fields[5])
	if err != nil {
		return err
	}
	socketIdentity, err := parseCapability(fields[6])
	if err != nil {
		return err
	}
	processMetadata, err := parseCapability(fields[7])
	if err != nil {
		return err
	}
	budget, err := parseObservationBudget(fields[8], fields[9], fields[10], fields[11])
	if err != nil {
		return err
	}
	p.active = true
	p.sequence = sequence
	p.boot = boot
	p.network = network
	p.capability = core.DiscoveryCapability{
		RemoteListeners: remoteListeners,
		SocketIdentity:  socketIdentity,
		ProcessMetadata: processMetadata,
	}
	p.budget = budget
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
	if !p.active || len(fields) != 7 || fields[2] != strconv.FormatUint(p.sequence, 10) || len(p.listeners) >= p.budget.Sockets {
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
	if !p.active || len(fields) != 10 || fields[2] != strconv.FormatUint(p.sequence, 10) || p.processCount >= p.budget.ProcessRecords {
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
	recordSize, err := encodedMetadataSize(fields[7], fields[8], fields[9])
	if err != nil || p.metadataSize+recordSize > p.budget.MetadataBytes {
		return errInvalidScannerFrame
	}
	executable, executableAvailable, err := decodeMetadataText(fields[7])
	if err != nil {
		return err
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
		key := fmt.Sprintf("%s\x00%s\x00%05d", listener.family, listener.scope, listener.port)
		observation := observations[key]
		if observation == nil {
			if len(observations) >= p.budget.Listeners {
				return core.ObservationSet{}, errInvalidScannerFrame
			}
			observation = &core.ListenerObservation{
				Family:     listener.family,
				BindScope:  listener.scope,
				RemotePort: listener.port,
			}
			observations[key] = observation
		}
		if p.capability.SocketIdentity != core.CapabilityUnavailable && p.boot != "unavailable" && p.network != "unavailable" && listener.inode != "0" {
			observation.SocketIdentities = append(observation.SocketIdentities, socketIdentity(p.boot, p.network, listener))
		}
		observation.Processes = append(observation.Processes, p.processChains(listener.inode)...)
	}
	items := make([]core.ListenerObservation, 0, len(observations))
	for _, observation := range observations {
		items = append(items, *observation)
	}
	set := core.ObservationSet{
		Sequence:        p.sequence,
		ScannerVersion:  scannerVersion,
		ScannerChecksum: embeddedScannerChecksum,
		Capability:      p.capability,
		Budget:          p.budget,
		Observations:    items,
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
	p.boot = ""
	p.network = ""
	p.capability = core.DiscoveryCapability{}
	p.budget = core.ObservationBudget{}
	p.listeners = nil
	p.listenerKeys = nil
	p.inodeListeners = nil
	p.inodes = nil
	p.processes = nil
	p.processCount = 0
	p.metadataSize = 0
}

// parseObservationBudget validates the declared evidence budget: each
// dimension must be at least one and within the scanner's own frame limits, so
// a mismatched script cannot silently change what core may retain.
func parseObservationBudget(listeners, sockets, processRecords, metadataBytes string) (core.ObservationBudget, error) {
	parse := func(value string, maximum uint64) (int, error) {
		parsed, err := parseUint(value, 32)
		if err != nil || parsed == 0 || parsed > maximum {
			return 0, errInvalidScannerFrame
		}
		return int(parsed), nil
	}
	budget := core.ObservationBudget{}
	var err error
	if budget.Listeners, err = parse(listeners, uint64(MaxObservedListeners)); err != nil {
		return core.ObservationBudget{}, err
	}
	if budget.Sockets, err = parse(sockets, uint64(MaxObservedSockets)); err != nil {
		return core.ObservationBudget{}, err
	}
	if budget.ProcessRecords, err = parse(processRecords, uint64(MaxProcessRecords)); err != nil {
		return core.ObservationBudget{}, err
	}
	if budget.MetadataBytes, err = parse(metadataBytes, uint64(MaxObservationMetadataBytes)); err != nil {
		return core.ObservationBudget{}, err
	}
	return budget, nil
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
		SocketIdentity:  core.CapabilityUnavailable,
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

func decodeText(value string, maximum int, allowEmpty bool) (string, error) {
	if len(value) > maximum*2 || len(value)%2 != 0 {
		return "", errInvalidScannerFrame
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) > maximum || (!allowEmpty && len(decoded) == 0) || !utf8.Valid(decoded) || strings.IndexByte(string(decoded), 0) >= 0 {
		return "", errInvalidScannerFrame
	}
	return string(decoded), nil
}

func encodedMetadataSize(values ...string) (int, error) {
	total := 0
	for _, value := range values {
		if len(value) > maxProcessTextBytes*2 || len(value)%2 != 0 {
			return 0, errInvalidScannerFrame
		}
		total += len(value) / 2
	}
	return total, nil
}

func decodeMetadataText(value string) (string, bool, error) {
	if len(value) > maxProcessTextBytes*2 || len(value)%2 != 0 {
		return "", false, errInvalidScannerFrame
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) > maxProcessTextBytes {
		return "", false, errInvalidScannerFrame
	}
	if len(decoded) == 0 || !utf8.Valid(decoded) || strings.IndexByte(string(decoded), 0) >= 0 {
		return "", false, nil
	}
	return string(decoded), true, nil
}

func decodeArguments(value string) ([]string, bool, error) {
	if len(value) > maxProcessTextBytes*2 || len(value)%2 != 0 {
		return nil, false, errInvalidScannerFrame
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) > maxProcessTextBytes {
		return nil, false, errInvalidScannerFrame
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

func socketIdentity(boot, network string, listener scannerListener) core.SocketIdentity {
	digest := sha256.Sum256([]byte(strings.Join([]string{boot, network, string(listener.family), listener.inode}, "\x00")))
	return core.SocketIdentity("socket:" + hex.EncodeToString(digest[:]))
}
