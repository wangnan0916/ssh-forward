package openssh

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestScannerBootstrapPreservesLinesAndRemainingStdin(t *testing.T) {
	command := exec.Command("sh", "-c", scannerBootstrap)
	command.Stdin = strings.NewReader("printf '%s\\n' first\nIFS= read -r line || exit 1\nprintf '%s\\n' \"$line\"\n" + scannerScriptEnd + "\nsecond\n")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "bootstrap failed: %s", output)
	require.Equal(t, "first\nsecond\n", string(output))
}

func TestScanListenersInvalidatesSSHCacheOnSocketChange(t *testing.T) {
	original := snapshotSocket{Addr: procV4Loopback, Port: 8080, UID: "1000", Inode: 100}
	for _, test := range []struct {
		name  string
		addr  string
		inode uint64
		app   string
	}{
		{name: "new inode without app", addr: procV4Loopback, inode: 200},
		{name: "new inode with known app", addr: procV4Loopback, inode: 200, app: "node"},
		{name: "new address without app", addr: procV4Wildcard, inode: 100},
		{name: "new address with known app", addr: procV4Wildcard, inode: 100, app: "node"},
	} {
		t.Run(test.name, func(t *testing.T) {
			replacement := original
			replacement.Addr, replacement.Inode = test.addr, test.inode
			first := capabilitySnapshot{UID: "1000", Sockets: []snapshotSocket{original}}
			next := capabilitySnapshot{UID: "1000", Sockets: []snapshotSocket{replacement}}
			if test.app != "" {
				next.Owners = []snapshotOwner{{Inode: replacement.Inode, PID: 42}}
				next.Processes = []snapshotProcess{{PID: 42, Exe: "/usr/bin/" + test.app, Cwd: "/srv/app"}}
			}

			var input, requests bytes.Buffer
			encoder := json.NewEncoder(&input)
			require.NoError(t, encoder.Encode(first))
			require.NoError(t, encoder.Encode(bannerSnapshot{Banners: []snapshotBanner{{Port: 8080, Banner: "SSH-2.0-test"}}}))
			// The identical socket stays cached and must not be probed again.
			require.NoError(t, encoder.Encode(first))
			require.NoError(t, encoder.Encode(bannerSnapshot{}))
			require.NoError(t, encoder.Encode(next))
			require.NoError(t, encoder.Encode(bannerSnapshot{Banners: []snapshotBanner{{Port: 8080, Banner: "HTTP/1.1 200 OK"}}}))

			var observed [][]core.Listener
			require.NoError(t, scanListeners(&input, &requests, func(listeners []core.Listener) {
				observed = append(observed, listeners)
			}))
			require.Len(t, observed, 3)
			require.Empty(t, observed[0])
			require.Empty(t, observed[1])
			expected := core.Listener{Port: 8080, App: test.app}
			if test.app == "" {
				require.Equal(t, "8080\n\n8080\n", requests.String())
			} else {
				expected.WorkingDirectory = "/srv/app"
				require.Equal(t, "8080\n\n\n", requests.String())
			}
			require.Equal(t, []core.Listener{expected}, observed[2])
		})
	}
}

func TestSSHCacheRetainsIdentityAndClearsDisappearedSocket(t *testing.T) {
	socket := snapshotSocket{Addr: procV4Loopback, Port: 8080, UID: "1000", Inode: 100}
	snapshot := capabilitySnapshot{UID: "1000", Sockets: []snapshotSocket{socket}}
	knownSSH := make(map[uint16]snapshotSocket)
	listeners, probe, present := snapshot.classify(knownSSH)
	require.Equal(t, []uint16{8080}, probe)
	listeners = applyBanners(listeners, []snapshotBanner{{Port: 8080, Banner: "SSH-2.0-test"}}, probe, knownSSH, present)
	require.Empty(t, listeners)
	require.Equal(t, map[uint16]snapshotSocket{8080: socket}, knownSSH)

	listeners, probe, present = snapshot.classify(knownSSH)
	require.Empty(t, listeners)
	require.Empty(t, probe)
	require.Empty(t, applyBanners(listeners, nil, probe, knownSSH, present))
	require.Equal(t, map[uint16]snapshotSocket{8080: socket}, knownSSH)

	listeners, probe, present = (capabilitySnapshot{UID: "1000"}).classify(knownSSH)
	require.Empty(t, applyBanners(listeners, nil, probe, knownSSH, present))
	require.Empty(t, knownSSH)

	// Even the old identity must be probed again after an observed absence.
	listeners, probe, present = snapshot.classify(knownSSH)
	require.Equal(t, []uint16{8080}, probe)
	require.Equal(t, []core.Listener{{Port: 8080}}, applyBanners(listeners, nil, probe, knownSSH, present))
}
