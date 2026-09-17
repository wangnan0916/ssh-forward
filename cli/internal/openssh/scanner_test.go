package openssh

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestScanListenerFrames(t *testing.T) {
	metadata := base64.StdEncoding.EncodeToString([]byte("node\x00/workspace/app"))
	input := "PF2\tB\t1\nPF2\tP\t1\t8080\tAA==\nPF2\tP\t1\t5173\t" + metadata + "\nPF2\tE\t1\n" +
		"PF2\tB\t2\nPF2\tE\t2\n"
	var observations [][]core.Listener
	err := scanListenerFrames(strings.NewReader(input), func(listeners []core.Listener) {
		observations = append(observations, listeners)
	})
	require.NoError(t, err)
	want := [][]core.Listener{{{Port: 5173, App: "node", WorkingDirectory: "/workspace/app"}, {Port: 8080}}, {}}
	require.Equal(t, want, observations)
}

func TestScanListenerFramesRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{
		"PF2\tB\t2\nPF2\tP\t1\t8080\tAA==\n",             // mismatched sequence
		"PF2\tB\t1\n",                                    // truncated snapshot
		"PF2\tB\t1\nPF2\tP\t1\t8080\tbm8gc2VwYXJhdG9y\n", // missing NUL metadata separator
	} {
		require.Error(t, scanListenerFrames(strings.NewReader(input), func([]core.Listener) {}))
	}
}

func TestScanListenerFramesSanitizesTerminalControlCharacters(t *testing.T) {
	metadata := base64.StdEncoding.EncodeToString([]byte("node\x1b[31m\x00/work\napp"))
	input := "PF2\tB\t1\nPF2\tP\t1\t8080\t" + metadata + "\nPF2\tE\t1\n"
	var got []core.Listener
	require.NoError(t, scanListenerFrames(strings.NewReader(input), func(listeners []core.Listener) {
		got = listeners
	}))
	want := []core.Listener{{Port: 8080, App: "node�[31m", WorkingDirectory: "/work�app"}}
	require.Equal(t, want, got)
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
