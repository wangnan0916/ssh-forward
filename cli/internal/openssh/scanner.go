package openssh

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	maxScannerLineBytes       = 1 << 20
	maxObservedPorts          = 256
	maxObservedAppBytes       = 255
	maxObservedDirectoryBytes = 768
)

var errInvalidScannerSnapshot = errors.New("invalid scanner snapshot")

// /proc/net/tcp prints addresses as uppercase hex. IPv4 values are little-endian.
const (
	procV4Loopback = "0100007F"
	procV4Wildcard = "00000000"
	procV6Wildcard = "00000000000000000000000000000000"
	procV4Mapped   = "0000000000000000FFFF00000100007F"
)

// capabilitySnapshot is one remote observation. Empty arrays mean that source
// was unavailable. Inode and pid are strings so values past the float64 range survive.
type capabilitySnapshot struct {
	UID       string            `json:"uid"`
	V6        string            `json:"v6"`
	Sockets   []snapshotSocket  `json:"sockets"`
	Owners    []snapshotOwner   `json:"owners"`
	Processes []snapshotProcess `json:"processes"`
	Docker    []snapshotDocker  `json:"docker"`
}

// procPort is the hexadecimal port column from /proc/net/tcp. A bad value stays 0.
type procPort uint16

func (port *procPort) UnmarshalJSON(data []byte) error {
	if value, err := strconv.ParseUint(strings.Trim(string(data), `"`), 16, 16); err == nil {
		*port = procPort(value)
	}
	return nil
}

func (port procPort) MarshalJSON() ([]byte, error) {
	return strconv.AppendQuote(nil, strconv.FormatUint(uint64(port), 16)), nil
}

type snapshotSocket struct {
	Addr  string   `json:"addr"`
	Port  procPort `json:"port"`
	UID   string   `json:"uid"`
	Inode uint64   `json:"inode,string"`
}

type snapshotOwner struct {
	Inode uint64 `json:"inode,string"`
	PID   uint64 `json:"pid,string"`
}

type snapshotProcess struct {
	PID uint64 `json:"pid,string"`
	Exe string `json:"exe"`
	Cwd string `json:"cwd"`
}

type snapshotDocker struct {
	Port    uint16 `json:"port"`
	Service string `json:"service"`
	Dir     string `json:"dir"`
	Name    string `json:"name"`
}

type snapshotBanner struct {
	Port   uint16 `json:"port"`
	Banner string `json:"banner"`
}

type bannerSnapshot struct {
	Banners []snapshotBanner `json:"banners"`
}

func scanListeners(stdout io.Reader, stdin io.Writer, emit func([]core.Listener)) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 256), maxScannerLineBytes)
	knownSSH := make(map[uint16]struct{})
	for scanner.Scan() {
		var snapshot capabilitySnapshot
		if len(scanner.Bytes()) == 0 || json.Unmarshal(scanner.Bytes(), &snapshot) != nil || snapshot.UID == "" {
			return errInvalidScannerSnapshot
		}
		listeners, probe, present := snapshot.classify(knownSSH)
		text := make([]string, len(probe))
		for index, port := range probe {
			text[index] = strconv.Itoa(int(port))
		}
		if _, err := io.WriteString(stdin, strings.Join(text, " ")+"\n"); err != nil {
			return err
		}
		if !scanner.Scan() {
			return cmp.Or(scanner.Err(), errInvalidScannerSnapshot)
		}
		var banners bannerSnapshot
		if len(scanner.Bytes()) == 0 || json.Unmarshal(scanner.Bytes(), &banners) != nil {
			return errInvalidScannerSnapshot
		}
		emit(applyBanners(listeners, banners.Banners, probe, knownSSH, present))
	}
	return scanner.Err()
}

func (snapshot capabilitySnapshot) classify(knownSSH map[uint16]struct{}) ([]core.Listener, []uint16, map[uint16]struct{}) {
	owners := make(map[uint64]uint64, len(snapshot.Owners))
	for _, owner := range snapshot.Owners {
		if owner.Inode != 0 && owner.PID != 0 && owners[owner.Inode] == 0 {
			owners[owner.Inode] = owner.PID
		}
	}
	processes := make(map[uint64]snapshotProcess, len(snapshot.Processes))
	for _, process := range snapshot.Processes {
		if process.PID != 0 && (process.Exe != "" || process.Cwd != "") {
			if _, exists := processes[process.PID]; !exists {
				processes[process.PID] = process
			}
		}
	}
	published := make(map[uint16]snapshotDocker, len(snapshot.Docker))
	for _, item := range snapshot.Docker {
		if item.Port != 0 && published[item.Port].Port == 0 {
			published[item.Port] = item
		}
	}

	best := make(map[uint16]snapshotSocket)
	for _, socket := range snapshot.Sockets {
		if !socketReachable(socket, snapshot.UID, snapshot.V6) {
			continue
		}
		port := uint16(socket.Port)
		if current, ok := best[port]; !ok || socket.Inode < current.Inode {
			best[port] = socket
		}
	}
	reachable := slices.SortedFunc(maps.Values(best), func(left, right snapshotSocket) int {
		return int(left.Port) - int(right.Port)
	})
	if len(reachable) > maxObservedPorts {
		reachable = reachable[:maxObservedPorts]
	}

	listeners := make([]core.Listener, 0, len(reachable))
	probe := make([]uint16, 0)
	present := make(map[uint16]struct{}, len(reachable))
	for _, socket := range reachable {
		port := uint16(socket.Port)
		present[port] = struct{}{}
		if _, cached := knownSSH[port]; cached {
			continue
		}
		process := processes[owners[socket.Inode]]
		app, directory := "", process.Cwd
		if process.Exe != "" {
			app = path.Base(process.Exe)
		}
		if app == "" && directory == "" {
			if label, ok := published[port]; ok {
				app = cmp.Or(label.Service, strings.TrimPrefix(label.Name, "/"))
				directory = label.Dir
			}
		}
		if app == "sshd" {
			continue
		}
		listeners = append(listeners, core.Listener{
			Port:             port,
			App:              sanitizeMetadata(app, maxObservedAppBytes),
			WorkingDirectory: sanitizeMetadata(directory, maxObservedDirectoryBytes),
		})
		if app == "" {
			probe = append(probe, port)
		}
	}
	return listeners, probe, present
}

func applyBanners(listeners []core.Listener, banners []snapshotBanner, probe []uint16, knownSSH, present map[uint16]struct{}) []core.Listener {
	for _, banner := range banners {
		if slices.Contains(probe, banner.Port) && strings.HasPrefix(banner.Banner, "SSH-") {
			knownSSH[banner.Port] = struct{}{}
		}
	}
	for port := range knownSSH {
		if _, ok := present[port]; !ok {
			delete(knownSSH, port)
		}
	}
	return slices.DeleteFunc(listeners, func(listener core.Listener) bool {
		_, skip := knownSSH[listener.Port]
		return skip
	})
}

func socketReachable(socket snapshotSocket, self, v6 string) bool {
	if socket.Port == 0 {
		return false
	}
	address := strings.ToUpper(socket.Addr)
	if len(address) > len(procV4Loopback) {
		return v6 == "0" && (address == procV4Mapped || (address == procV6Wildcard && socket.UID == self))
	}
	return address == procV4Loopback || (address == procV4Wildcard && socket.UID == self)
}

func sanitizeMetadata(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return strings.ToValidUTF8(value[:max], "�")
}
