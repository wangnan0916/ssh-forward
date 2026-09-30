package openssh

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestScanListeners(t *testing.T) {
	first := capabilitySnapshot{
		UID: "1000",
		V6:  "0",
		Sockets: []snapshotSocket{
			{Addr: procV4Loopback, Port: 8080, UID: "1000", Inode: 50},
			{Addr: procV4Loopback, Port: 8080, UID: "1000", Inode: 10},
			{Addr: procV4Wildcard, Port: 80, UID: "1000", Inode: 11},
			{Addr: procV4Wildcard, Port: 22, UID: "0", Inode: 12},
			{Addr: procV4Loopback, Port: 2200, UID: "1000", Inode: 13},
			{Addr: procV4Loopback, Port: 2201, UID: "1000", Inode: 14},
			{Addr: procV4Loopback, Port: 55432, UID: "1000", Inode: 15},
			{Addr: procV4Mapped, Port: 443, UID: "1", Inode: 16},
			{Addr: procV6Wildcard, Port: 9090, UID: "1000", Inode: 17},
			{Addr: procV6Wildcard, Port: 9091, UID: "0", Inode: 18},
			{Addr: procV4Loopback, Port: 58333, UID: "1000", Inode: 19},
		},
		Owners: []snapshotOwner{
			{Inode: 10, PID: 7},
			{Inode: 10, PID: 8},
			{Inode: 13, PID: 9},
		},
		Processes: []snapshotProcess{
			{PID: 7, Exe: "/usr/local/bin/node", Cwd: "/workspace/app"},
			{PID: 8, Exe: "/usr/local/bin/loser", Cwd: "/other"},
			{PID: 9, Exe: "/usr/sbin/sshd", Cwd: "/var/empty"},
		},
		Docker: []snapshotDocker{
			{Port: 8080, Service: "ignored", Dir: "/docker", Name: "/ignored"},
			{Port: 55432, Service: "postgres", Dir: "/home/shampoo/Workspace/personal/demo", Name: "/content-pages-postgres-1"},
			{Port: 58333, Dir: "/work", Name: "/content-pages-s3-1"},
		},
	}
	again := capabilitySnapshot{
		UID: "1000",
		V6:  "1",
		Sockets: []snapshotSocket{
			{Addr: procV4Loopback, Port: 2201, UID: "1000", Inode: 14},
			{Addr: procV4Mapped, Port: 443, UID: "1000", Inode: 16},
		},
	}
	got, probes := scanDialogue(t,
		[]capabilitySnapshot{first, again},
		[]string{
			`{"banners":[{"port":80,"banner":"HTTP/1.0"},{"port":443,"banner":""},{"port":2201,"banner":"SSH-2.0-OpenSSH"},{"port":9090,"banner":"x"}]}`,
			`{"banners":[]}`,
		},
	)
	require.Equal(t, [][]core.Listener{
		{
			{Port: 80},
			{Port: 443},
			{Port: 8080, App: "node", WorkingDirectory: "/workspace/app"},
			{Port: 9090},
			{Port: 55432, App: "postgres", WorkingDirectory: "/home/shampoo/Workspace/personal/demo"},
			{Port: 58333, App: "content-pages-s3-1", WorkingDirectory: "/work"},
		},
		{},
	}, got)
	require.Equal(t, "80 443 2201 9090\n\n", probes)
}

func TestScanListenersAcceptsShellObjectShape(t *testing.T) {
	input := `{"uid":"1000","v6":"0","sockets":[{"addr":"0100007F","port":"1F90","uid":"1000","inode":"10"}],"owners":[{"inode":"10","pid":"7"}],"processes":[{"pid":"7","exe":"/usr/local/bin/node","cwd":"/workspace/app"}],"docker":[]}` +
		"\n{\"banners\":[]}\n"
	var got []core.Listener
	require.NoError(t, scanListeners(strings.NewReader(input), io.Discard, func(listeners []core.Listener) {
		got = listeners
	}))
	require.Equal(t, []core.Listener{{Port: 8080, App: "node", WorkingDirectory: "/workspace/app"}}, got)
}

func TestScanListenersDoesNotBackfillPastThePortCap(t *testing.T) {
	sockets := make([]snapshotSocket, 0, maxObservedPorts+1)
	for port := 1; port <= maxObservedPorts+1; port++ {
		sockets = append(sockets, snapshotSocket{
			Addr: procV4Loopback, Port: procPort(port), UID: "1000", Inode: uint64(port),
		})
	}
	got, probes := scanDialogue(t,
		[]capabilitySnapshot{{UID: "1000", V6: "1", Sockets: sockets}},
		[]string{`{"banners":[{"port":1,"banner":"SSH-2.0-"}]}`},
	)
	require.Len(t, got[0], maxObservedPorts-1)
	require.Equal(t, uint16(2), got[0][0].Port)
	require.Equal(t, uint16(maxObservedPorts), got[0][len(got[0])-1].Port)
	fields := strings.Fields(probes)
	require.Equal(t, strconv.Itoa(1), fields[0])
	require.NotContains(t, fields, strconv.Itoa(maxObservedPorts+1))
}

func TestScanListenersRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{
		"\n",
		"not-json\n",
		"{}\n",
		"{\"uid\":\"1000\"\n",
		"{\"uid\":\"1000\",\"v6\":\"1\"}\n",
		"{\"uid\":\"1000\",\"v6\":\"1\"}\nnot-json\n",
	} {
		require.ErrorIs(t, scanListeners(strings.NewReader(input), io.Discard, func([]core.Listener) {}), errInvalidScannerSnapshot)
	}
}

func TestScanListenersTruncatesMetadata(t *testing.T) {
	app := strings.Repeat("a", maxObservedAppBytes+4)
	directory := strings.Repeat("b", maxObservedDirectoryBytes-1) + "é"
	got, _ := scanDialogue(t, []capabilitySnapshot{{
		UID:     "1000",
		V6:      "1",
		Sockets: []snapshotSocket{{Addr: procV4Loopback, Port: 8080, UID: "1000", Inode: 10}},
		Owners:  []snapshotOwner{{Inode: 10, PID: 7}},
		Processes: []snapshotProcess{{
			PID: 7, Exe: "/bin/" + app, Cwd: directory,
		}},
	}}, nil)
	require.Equal(t, strings.Repeat("a", maxObservedAppBytes), got[0][0].App)
	require.Equal(t, strings.Repeat("b", maxObservedDirectoryBytes-1)+"�", got[0][0].WorkingDirectory)
}

func TestScannerBootstrapPreservesLinesAndRemainingStdin(t *testing.T) {
	command := exec.Command("sh", "-c", scannerBootstrap)
	command.Stdin = strings.NewReader("printf '%s\\n' first\nIFS= read -r line || exit 1\nprintf '%s\\n' \"$line\"\n" + scannerScriptEnd + "\nsecond\n")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "bootstrap failed: %s", output)
	require.Equal(t, "first\nsecond\n", string(output))
}

func TestClassifyError(t *testing.T) {
	tests := map[string]string{
		"Permission denied (publickey).":       "authentication_failed",
		"Host key verification failed.":        "host_key_failed",
		"bind: Address already in use":         "local_port_conflict",
		"ssh: connect to host dev port 22: no": "transport_unavailable",
	}
	for stderr, want := range tests {
		err := classifyError(errors.New("failed"), stderr)
		if err.Error() != want {
			t.Errorf("classifyError(%q) = %q, want %q", stderr, err, want)
		}
	}
}

func TestValidAlias(t *testing.T) {
	for _, alias := range []string{"dev", "user@host", "dev.example", "192.168.1.20", "ubuntu@192.168.1.20"} {
		if !core.ValidHostName(alias) {
			t.Errorf("core.ValidHostName(%q) = false", alias)
		}
	}
	for _, alias := range []string{"", "-oProxyCommand=bad", "two words", "line\nbreak"} {
		if core.ValidHostName(alias) {
			t.Errorf("core.ValidHostName(%q) = true", alias)
		}
	}
}

func scanDialogue(t *testing.T, snapshots []capabilitySnapshot, banners []string) ([][]core.Listener, string) {
	t.Helper()
	var input strings.Builder
	for index, snapshot := range snapshots {
		raw, err := json.Marshal(snapshot)
		require.NoError(t, err)
		input.Write(raw)
		input.WriteByte('\n')
		banner := `{"banners":[]}`
		if index < len(banners) && banners[index] != "" {
			banner = banners[index]
		}
		input.WriteString(banner)
		input.WriteByte('\n')
	}
	var probes strings.Builder
	var got [][]core.Listener
	require.NoError(t, scanListeners(strings.NewReader(input.String()), &probes, func(listeners []core.Listener) {
		got = append(got, listeners)
	}))
	return got, probes.String()
}
