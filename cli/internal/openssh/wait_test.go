package openssh_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"errors"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

func TestSessionWaitBoundsInheritedDiagnosticPipe(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "ssh")
	childPath := filepath.Join(directory, "child-pid")
	script := fmt.Sprintf(`#!/usr/bin/python3
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
child = subprocess.Popen(["/bin/sleep", "5"])
with open(%s, "w") as output:
    output.write(str(child.pid))
time.sleep(0.1)
listener.close()
`, strconv.Quote(childPath))
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
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Close(cleanupCtx)
	})
	// Session termination is observed through the stream: Next returns the
	// terminal SessionError once the session actually ends.
	terminationCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Consume trailing facts until the terminal SessionError appears.
	var sessionError *core.SessionError
	for {
		_, err := session.Next(terminationCtx)
		if errors.As(err, &sessionError) {
			break
		}
		if err == nil {
			continue
		}
		t.Fatalf("Next after Close = %v, want terminal SessionError", err)
	}
	contents, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatalf("read descendant PID: %v", err)
	}
	childPID, err := strconv.Atoi(string(contents))
	if err != nil {
		t.Fatalf("parse descendant PID: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Close(closeCtx); err != nil {
		t.Fatalf("close completed Session: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(childPID, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("descendant %d survived Session.Close", childPID)
		}
		time.Sleep(time.Millisecond)
	}
}
