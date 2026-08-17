package openssh_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"

	"github.com/google/go-cmp/cmp"
)

func TestSessionReturnsValidatedListenerObservation(t *testing.T) {
	directory := t.TempDir()
	scannerPath := filepath.Join(directory, "scanner")
	executable := filepath.Join(directory, "ssh")
	hexText := func(value string) string { return hex.EncodeToString([]byte(value)) }
	bootFrame := func(sequence int) string {
		return fmt.Sprintf("SF1\tB\t%d\t%s\t%s\tfull\tfull\tpartial\t%d\t%d\t%d\t%d\n",
			sequence, hexText("boot-1"), hexText("net:[42]"),
			openssh.MaxObservedListeners, openssh.MaxObservedSockets, openssh.MaxProcessRecords, openssh.MaxObservationMetadataBytes)
	}
	var queued strings.Builder
	for sequence := 2; sequence <= 12; sequence++ {
		queued.WriteString(bootFrame(sequence))
		fmt.Fprintf(&queued, "SF1\tE\t%d\n", sequence)
	}
	queued.WriteString("invalid-one\n")
	queued.WriteString(bootFrame(13))
	queued.WriteString("invalid-two\n")
	queued.WriteString(bootFrame(14))
	queued.WriteString("invalid-three\n")
	queued.WriteString(bootFrame(15))
	script := fmt.Sprintf(`#!/usr/bin/python3
import json
import signal
import socket
import sys

arguments = sys.argv[1:]
scanner = sys.stdin.read()
with open(%s, "w") as output:
    output.write(scanner)
dynamic = arguments[arguments.index("-D") + 1]
port = int(dynamic.rsplit(":", 1)[1])
listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", port))
listener.listen()
listener.settimeout(0.1)
print("invalid", flush=True)
print(%s, flush=True)
print(%s, flush=True)
print(%s, flush=True)
print(%s, flush=True)
sys.stdout.write(%s)
sys.stdout.flush()
running = True
def stop(_signum, _frame):
    global running
    running = False
signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
while running:
    try:
        connection, _ = listener.accept()
    except TimeoutError:
        continue
    with connection:
        greeting = b""
        while len(greeting) < 3:
            chunk = connection.recv(3 - len(greeting))
            if not chunk:
                break
            greeting += chunk
        if greeting == bytes([5, 1, 0]):
            connection.sendall(bytes([5, 0]))
listener.close()
`,
		strconv.Quote(scannerPath),
		strconv.Quote(strings.Join([]string{"SF1", "B", "1", hexText("boot-1"), hexText("net:[42]"), "full", "full", "partial", "256", "256", "512", "131072"}, "\t")),
		strconv.Quote(strings.Join([]string{"SF1", "L", "1", "ipv4", "loopback", "38080", "12345"}, "\t")),
		strconv.Quote(strings.Join([]string{"SF1", "P", "1", "12345", "42", "0", "42", hexText("/usr/bin/python3"), hexText("/workspace"), hexText("python3\x00fixture.py\x00")}, "\t")),
		strconv.Quote(strings.Join([]string{"SF1", "E", "1"}, "\t")),
		strconv.Quote(queued.String()),
	)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable, ReadyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session, err := adapter.Start(context.Background(), "development")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fact, err := session.Next(ctx)
	if err != nil {
		t.Fatalf("degraded Next: %v", err)
	}
	change, ok := fact.(core.DiscoveryChange)
	if !ok || change.State != core.DiscoveryDegraded || change.Reason != core.ReasonFrameInvalid {
		t.Fatalf("first invalid frame fact = %#v, want degraded DiscoveryChange", fact)
	}
	fact, err = session.Next(ctx)
	if err != nil {
		t.Fatalf("observation Next: %v", err)
	}
	observationSet, ok := fact.(core.ObservationSet)
	if !ok {
		t.Fatalf("fact = %#v, want ObservationSet", fact)
	}
	wantCapability := core.DiscoveryCapability{
		RemoteListeners: core.CapabilityFull,
		SocketIdentity:  core.CapabilityFull,
		ProcessMetadata: core.CapabilityPartial,
	}
	if observationSet.Sequence != 1 || !cmp.Equal(observationSet.Capability, wantCapability) {
		t.Fatalf("ObservationSet mismatch (-got +want):\n%s", cmp.Diff(observationSet.Capability, wantCapability))
	}
	if observationSet.ScannerVersion != 1 || len(observationSet.ScannerChecksum) != 64 {
		t.Fatalf("scanner identity = version %d checksum %q", observationSet.ScannerVersion, observationSet.ScannerChecksum)
	}
	if len(observationSet.Observations) != 1 {
		t.Fatalf("Listener Observations = %#v, want one", observationSet.Observations)
	}
	observation := observationSet.Observations[0]
	if observation.Family != core.FamilyIPv4 || observation.BindScope != core.BindLoopback || observation.RemotePort != 38080 {
		t.Fatalf("Listener Observation = %#v", observation)
	}
	if len(observation.SocketIdentities) != 1 || !strings.HasPrefix(string(observation.SocketIdentities[0]), "socket:") {
		t.Fatalf("Socket Identities = %#v, want one opaque identity", observation.SocketIdentities)
	}
	wantProcesses := []core.ProcessChain{{Processes: []core.ProcessMetadata{{
		PID:              42,
		Executable:       "/usr/bin/python3",
		WorkingDirectory: "/workspace",
		Arguments:        []string{"python3", "fixture.py"},
	}}}}
	if diff := cmp.Diff(observation.Processes, wantProcesses); diff != "" {
		t.Fatalf("Process Chains mismatch (-got +want):\n%s", diff)
	}
	scanner, err := os.ReadFile(scannerPath)
	if err != nil {
		t.Fatalf("read streamed scanner: %v", err)
	}
	// The streamed script must be the fixed v1 /proc scanner: the SF1 frame
	// emission (the wire version, see scanner.sh header) and the /proc
	// listener path.
	if !strings.Contains(string(scanner), `printf 'SF1\tB\t`) || !strings.Contains(string(scanner), "/proc/net/tcp") {
		t.Fatalf("streamed scanner is not the fixed v1 /proc scanner: %q", scanner)
	}

	foundFailed := false
	for attempt := 0; attempt < 16; attempt++ {
		fact, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("queued fact %d Next: %v", attempt+1, err)
		}
		if change, ok := fact.(core.DiscoveryChange); ok && change.State == core.DiscoveryFailed {
			foundFailed = true
			break
		}
	}
	if !foundFailed {
		t.Fatal("terminal DiscoveryFailed fact was lost under bounded queue pressure")
	}
	quietContext, cancelQuiet := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelQuiet()
	if _, err := session.Next(quietContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next after failed discovery = %v, want deadline while Forwarding Session remains alive", err)
	}
}
