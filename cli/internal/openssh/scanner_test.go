package openssh

import (
	"reflect"
	"strings"
	"testing"
)

func TestScanPortFrames(t *testing.T) {
	input := "PF1\tB\t1\nPF1\tP\t1\t8080\nPF1\tP\t1\t5173\nPF1\tE\t1\n" +
		"PF1\tB\t2\nPF1\tE\t2\n"
	var observations [][]uint16
	err := scanPortFrames(strings.NewReader(input), func(ports []uint16) {
		observations = append(observations, ports)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]uint16{{5173, 8080}, {}}
	if !reflect.DeepEqual(observations, want) {
		t.Fatalf("observations = %#v, want %#v", observations, want)
	}
}

func TestScanPortFramesRejectsMalformedSequence(t *testing.T) {
	input := "PF1\tB\t2\nPF1\tP\t1\t8080\n"
	if err := scanPortFrames(strings.NewReader(input), func([]uint16) {}); err == nil {
		t.Fatal("malformed sequence was accepted")
	}
}

func TestScanPortFramesRejectsTruncatedObservation(t *testing.T) {
	if err := scanPortFrames(strings.NewReader("PF1\tB\t1\n"), func([]uint16) {}); err == nil {
		t.Fatal("truncated observation was accepted")
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
