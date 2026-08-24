package openssh

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	maxScannerFrameBytes      = 2048
	maxObservedPorts          = 256
	maxObservedAppBytes       = 255
	maxObservedDirectoryBytes = 768
)

var errInvalidScannerFrame = errors.New("invalid scanner frame")

type scannerParser struct {
	active       bool
	sequence     uint64
	lastSequence uint64
	listeners    map[uint16]core.Listener
}

func scanListenerFrames(reader io.Reader, emit func([]core.Listener)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 256), maxScannerFrameBytes)
	parser := &scannerParser{}
	for scanner.Scan() {
		listeners, complete, err := parser.accept(scanner.Text())
		if err != nil {
			return err
		}
		if complete {
			emit(listeners)
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

func (p *scannerParser) accept(line string) ([]core.Listener, bool, error) {
	fields := strings.Split(line, "\t")
	if len(fields) < 3 || fields[0] != "PF2" {
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
		p.listeners = make(map[uint16]core.Listener)
	case "P":
		if len(fields) != 5 || !p.active || sequence != p.sequence || len(p.listeners) >= maxObservedPorts {
			return nil, false, errInvalidScannerFrame
		}
		port, err := strconv.ParseUint(fields[3], 10, 16)
		if err != nil || port == 0 {
			return nil, false, errInvalidScannerFrame
		}
		app, directory, err := parseListenerMetadata(fields[4])
		if err != nil {
			return nil, false, err
		}
		p.listeners[uint16(port)] = core.Listener{
			Port:             uint16(port),
			App:              app,
			WorkingDirectory: directory,
		}
	case "E":
		if len(fields) != 3 || !p.active || sequence != p.sequence {
			return nil, false, errInvalidScannerFrame
		}
		listeners := make([]core.Listener, 0, len(p.listeners))
		for _, listener := range p.listeners {
			listeners = append(listeners, listener)
		}
		slices.SortFunc(listeners, func(left, right core.Listener) int {
			return int(left.Port) - int(right.Port)
		})
		p.active = false
		p.lastSequence = sequence
		return listeners, true, nil
	default:
		return nil, false, errInvalidScannerFrame
	}
	return nil, false, nil
}

func parseListenerMetadata(encoded string) (string, string, error) {
	metadata, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || bytes.Count(metadata, []byte{0}) != 1 {
		return "", "", errInvalidScannerFrame
	}
	app, directory, _ := bytes.Cut(metadata, []byte{0})
	if len(app) > maxObservedAppBytes || len(directory) > maxObservedDirectoryBytes {
		return "", "", errInvalidScannerFrame
	}
	return safeMetadata(app), safeMetadata(directory), nil
}

func safeMetadata(value []byte) string {
	valid := strings.ToValidUTF8(string(value), "�")
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, valid)
}
