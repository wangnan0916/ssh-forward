package openssh

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

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
	if err != nil {
		t.Fatal(err)
	}
	want := [][]core.Listener{{
		{Port: 5173, App: "node", WorkingDirectory: "/workspace/app"},
		{Port: 8080},
	}, {}}
	if !reflect.DeepEqual(observations, want) {
		t.Fatalf("observations = %#v, want %#v", observations, want)
	}
}

func TestScanListenerFramesRejectsMalformedSequence(t *testing.T) {
	input := "PF2\tB\t2\nPF2\tP\t1\t8080\tAA==\n"
	if err := scanListenerFrames(strings.NewReader(input), func([]core.Listener) {}); err == nil {
		t.Fatal("malformed sequence was accepted")
	}
}

func TestScanListenerFramesRejectsTruncatedObservation(t *testing.T) {
	if err := scanListenerFrames(strings.NewReader("PF2\tB\t1\n"), func([]core.Listener) {}); err == nil {
		t.Fatal("truncated observation was accepted")
	}
}

func TestScanListenerFramesRejectsInvalidMetadata(t *testing.T) {
	input := "PF2\tB\t1\nPF2\tP\t1\t8080\tbm8gc2VwYXJhdG9y\n"
	if err := scanListenerFrames(strings.NewReader(input), func([]core.Listener) {}); err == nil {
		t.Fatal("metadata without its NUL separator was accepted")
	}
}

func TestScanListenerFramesSanitizesTerminalControlCharacters(t *testing.T) {
	metadata := base64.StdEncoding.EncodeToString([]byte("node\x1b[31m\x00/work\napp"))
	input := "PF2\tB\t1\nPF2\tP\t1\t8080\t" + metadata + "\nPF2\tE\t1\n"
	var got []core.Listener
	if err := scanListenerFrames(strings.NewReader(input), func(listeners []core.Listener) {
		got = listeners
	}); err != nil {
		t.Fatal(err)
	}
	want := []core.Listener{{Port: 8080, App: "node�[31m", WorkingDirectory: "/work�app"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listeners = %#v, want %#v", got, want)
	}
}

func TestClassifyError(t *testing.T) {
	tests := map[string]string{
		"Permission denied (publickey).":       "authentication_failed",
		"Host key verification failed.":        "host_key_failed",
		"bind: Address already in use":         "local_port_conflict",
		"ssh: connect to host dev port 22: no": "transport_unavailable",
	}
	for stderr, want := range tests {
		err := classifyError(assertError{}, stderr)
		if err.Error() != want {
			t.Errorf("classifyError(%q) = %q, want %q", stderr, err, want)
		}
	}
}

type assertError struct{}

func (assertError) Error() string { return "failed" }

func TestValidAlias(t *testing.T) {
	for _, alias := range []string{"dev", "user@host", "dev.example"} {
		if !validAlias(alias) {
			t.Errorf("validAlias(%q) = false", alias)
		}
	}
	for _, alias := range []string{"", "-oProxyCommand=bad", "two words", "line\nbreak"} {
		if validAlias(alias) {
			t.Errorf("validAlias(%q) = true", alias)
		}
	}
}
