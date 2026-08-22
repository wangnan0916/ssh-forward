package openssh

import (
	"bufio"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
)

const (
	maxScannerFrameBytes = 1024
	maxObservedPorts     = 256
)

var errInvalidScannerFrame = errors.New("invalid scanner frame")

type scannerParser struct {
	active       bool
	sequence     uint64
	lastSequence uint64
	ports        map[uint16]struct{}
}

func scanPortFrames(reader io.Reader, emit func([]uint16)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 256), maxScannerFrameBytes)
	parser := &scannerParser{}
	for scanner.Scan() {
		ports, complete, err := parser.accept(scanner.Text())
		if err != nil {
			return err
		}
		if complete {
			emit(ports)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if parser.active {
		return errInvalidScannerFrame
	}
	return nil
}

func (p *scannerParser) accept(line string) ([]uint16, bool, error) {
	fields := strings.Split(line, "\t")
	if len(fields) < 3 || fields[0] != "PF1" {
		return nil, false, errInvalidScannerFrame
	}
	sequence, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil || sequence == 0 {
		return nil, false, errInvalidScannerFrame
	}
	switch fields[1] {
	case "B":
		if len(fields) != 3 || p.active || sequence <= p.lastSequence {
			return nil, false, errInvalidScannerFrame
		}
		p.active = true
		p.sequence = sequence
		p.ports = make(map[uint16]struct{})
	case "P":
		if len(fields) != 4 || !p.active || sequence != p.sequence || len(p.ports) >= maxObservedPorts {
			return nil, false, errInvalidScannerFrame
		}
		port, err := strconv.ParseUint(fields[3], 10, 16)
		if err != nil || port == 0 {
			return nil, false, errInvalidScannerFrame
		}
		p.ports[uint16(port)] = struct{}{}
	case "E":
		if len(fields) != 3 || !p.active || sequence != p.sequence {
			return nil, false, errInvalidScannerFrame
		}
		ports := make([]uint16, 0, len(p.ports))
		for port := range p.ports {
			ports = append(ports, port)
		}
		slices.Sort(ports)
		p.active = false
		p.lastSequence = sequence
		return ports, true, nil
	default:
		return nil, false, errInvalidScannerFrame
	}
	return nil, false, nil
}
