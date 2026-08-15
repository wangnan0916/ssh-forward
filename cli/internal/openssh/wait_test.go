package openssh_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-forward/cli/internal/openssh"
)

func TestSessionWaitBoundsInheritedDiagnosticPipe(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "ssh")
	script := `#!/usr/bin/python3
import socket
import subprocess
import sys
import time

arguments = sys.argv[1:]
dynamic = arguments[arguments.index("-D") + 1]
port = int(dynamic.rsplit(":", 1)[1])
listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", port))
listener.listen()
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
subprocess.Popen(["/bin/sleep", "5"])
time.sleep(0.1)
listener.close()
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{
		Executable:   executable,
		ReadyTimeout: 5 * time.Second,
		WaitDelay:    100 * time.Millisecond,
	})
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
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Session.Wait remained blocked on descendant-held stderr")
	}
}
