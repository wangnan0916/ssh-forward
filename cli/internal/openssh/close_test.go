package openssh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-forward/cli/internal/openssh"
)

func TestSessionCloseIsBoundedWhenOpenSSHIgnoresTerminate(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "ssh")
	script := `#!/usr/bin/python3
import signal
import socket
import sys

arguments = sys.argv[1:]
dynamic = arguments[arguments.index("-D") + 1]
port = int(dynamic.rsplit(":", 1)[1])
listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", port))
listener.listen()
signal.signal(signal.SIGTERM, signal.SIG_IGN)
while True:
    connection, _ = listener.accept()
    with connection:
        greeting = b""
        while len(greeting) < 3:
            chunk = connection.recv(3 - len(greeting))
            if not chunk:
                break
            greeting += chunk
        if greeting == bytes([5, 1, 0]):
            connection.sendall(bytes([5, 0]))
`
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = session.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Close exceeded its bound: %v", elapsed)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("OpenSSH process was not reaped after forced shutdown")
	}
}
