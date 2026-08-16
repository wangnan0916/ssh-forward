package openssh_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

func TestValidateAliasInvokesConfiguredOpenSSH(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments")
	executable := filepath.Join(directory, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + shellQuote(argumentsPath) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := adapter.ValidateAlias(context.Background(), "development"); err != nil {
		t.Fatalf("ValidateAlias: %v", err)
	}
	contents, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatalf("read OpenSSH arguments: %v", err)
	}
	got := strings.Fields(string(contents))
	want := []string{"-G", "development"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSSH arguments = %q, want %q", got, want)
	}
}

func TestValidateAliasUsesExplicitSSHConfig(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments")
	executable := filepath.Join(directory, "ssh")
	configPath := filepath.Join(directory, "config")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + shellQuote(argumentsPath) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable, ConfigFile: configPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := adapter.ValidateAlias(context.Background(), "development"); err != nil {
		t.Fatalf("ValidateAlias: %v", err)
	}
	contents, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatalf("read OpenSSH arguments: %v", err)
	}
	got := strings.Fields(string(contents))
	want := []string{"-F", configPath, "-G", "development"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSSH arguments = %q, want %q", got, want)
	}
}

func TestOpenSSHChildReceivesOnlyApprovedEnvironment(t *testing.T) {
	directory := t.TempDir()
	environmentPath := filepath.Join(directory, "environment")
	executable := filepath.Join(directory, "ssh")
	script := "#!/bin/sh\nenv >" + shellQuote(environmentPath) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	t.Setenv("SSH_FORWARD_SHOULD_NOT_LEAK", "secret")
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(directory, "agent.sock"))
	adapter, err := openssh.New(openssh.Options{Executable: executable})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := adapter.ValidateAlias(context.Background(), "development"); err != nil {
		t.Fatalf("ValidateAlias: %v", err)
	}
	contents, err := os.ReadFile(environmentPath)
	if err != nil {
		t.Fatalf("read child environment: %v", err)
	}
	environment := string(contents)
	if strings.Contains(environment, "SSH_FORWARD_SHOULD_NOT_LEAK=") {
		t.Fatal("OpenSSH child inherited an unapproved environment value")
	}
	if !strings.Contains(environment, "SSH_AUTH_SOCK="+filepath.Join(directory, "agent.sock")) {
		t.Fatal("OpenSSH child did not inherit SSH_AUTH_SOCK")
	}
	if !strings.Contains(environment, "HOME="+os.Getenv("HOME")) {
		t.Fatal("OpenSSH child did not inherit HOME")
	}
}

func TestStartLaunchesDedicatedDynamicForward(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments.json")
	executable := filepath.Join(directory, "ssh")
	script := fmt.Sprintf(`#!/usr/bin/python3
import json
import signal
import socket
import sys

arguments = sys.argv[1:]
with open(%s, "w") as output:
    json.dump(arguments, output)
dynamic = arguments[arguments.index("-D") + 1]
port = int(dynamic.rsplit(":", 1)[1])
listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", port))
listener.listen()
listener.settimeout(0.1)
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
`, strconv.Quote(argumentsPath))
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{
		Executable:   executable,
		ReadyTimeout: 5 * time.Second,
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
		if err := session.Close(ctx); err != nil {
			t.Errorf("close Session: %v", err)
		}
	})
	contents, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatalf("read OpenSSH arguments: %v", err)
	}
	var arguments []string
	if err := json.Unmarshal(contents, &arguments); err != nil {
		t.Fatalf("decode OpenSSH arguments: %v", err)
	}
	if len(arguments) != 12 {
		t.Fatalf("OpenSSH arguments = %q, want 12 arguments", arguments)
	}
	wantPrefix := []string{"-T", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes", "-D"}
	if !reflect.DeepEqual(arguments[:8], wantPrefix) {
		t.Fatalf("OpenSSH argument prefix = %q, want %q", arguments[:8], wantPrefix)
	}
	if !strings.HasPrefix(arguments[8], "127.0.0.1:") || strings.HasSuffix(arguments[8], ":0") {
		t.Fatalf("dynamic forwarding address = %q, want a private IPv4 loopback port", arguments[8])
	}
	wantSuffix := []string{"development", "sh", "-s"}
	if !reflect.DeepEqual(arguments[9:], wantSuffix) {
		t.Fatalf("OpenSSH argument suffix = %q, want %q", arguments[9:], wantSuffix)
	}
	for _, argument := range arguments {
		if argument == "ClearAllForwardings=yes" {
			t.Fatal("OpenSSH arguments contain ClearAllForwardings=yes")
		}
	}
}

func TestConnectReturnsCoreClassifiedAuthenticationFailure(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "ssh")
	script := `#!/bin/sh
for argument in "$@"; do
    [ "$argument" != "-G" ] || exit 0
done
printf '%s\n' 'Permission denied (publickey).' >&2
exit 255
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable, ReadyTimeout: 5 * time.Second, WaitDelay: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = adapter.Connect(context.Background(), core.HostAlias("development"))
	var sessionError *core.SessionError
	if !errors.As(err, &sessionError) || sessionError.Disposition != core.SessionSuspend || sessionError.Reason != core.SessionReasonAuthentication {
		t.Fatalf("Connect error = %#v, want suspended authentication SessionError", err)
	}
}

func TestStartClassifiesAuthenticationFailure(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' 'Permission denied (publickey).' >&2\nexit 255\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable, ReadyTimeout: 5 * time.Second, WaitDelay: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = adapter.Start(context.Background(), "development")
	var sessionError *core.SessionError
	if !errors.As(err, &sessionError) {
		t.Fatalf("Start error = %v, want SessionError", err)
	}
	if sessionError.Disposition != core.SessionSuspend || sessionError.Reason != core.SessionReasonAuthentication {
		t.Fatalf("Start error = %#v, want suspend with authentication reason", sessionError)
	}
}

func TestStartClassifiesTerminalAuthenticationFailureAfterLongDiagnostics(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "ssh")
	script := `#!/usr/bin/python3
import sys
sys.stderr.write("x" * 70000)
sys.stderr.write("\nPermission denied (publickey).\n")
sys.exit(255)
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable, ReadyTimeout: 5 * time.Second, WaitDelay: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = adapter.Start(context.Background(), "development")
	var sessionError *core.SessionError
	if !errors.As(err, &sessionError) || sessionError.Disposition != core.SessionSuspend ||
		sessionError.Reason != core.SessionReasonAuthentication {
		t.Fatalf("Start error = %v, want suspend with authentication reason", err)
	}
}

func TestValidateAliasRejectsUnsafeAliasBeforeOpenSSH(t *testing.T) {
	directory := t.TempDir()
	invokedPath := filepath.Join(directory, "invoked")
	executable := filepath.Join(directory, "ssh")
	script := "#!/bin/sh\n: >" + shellQuote(invokedPath) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted OpenSSH: %v", err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: executable})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, alias := range []string{"", "-proxy", "two words", strings.Repeat("a", 256)} {
		if err := adapter.ValidateAlias(context.Background(), alias); !errors.Is(err, openssh.ErrInvalidAlias) {
			t.Fatalf("ValidateAlias(%q) error = %v, want ErrInvalidAlias", alias, err)
		}
	}
	if _, err := os.Stat(invokedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenSSH invocation marker error = %v, want not-exist", err)
	}
}

// Mirrors the copy in scanner_bounds_test.go; Go's test-package isolation
// (package openssh_test vs openssh) forces two definitions of this shell
// quoting helper, so keep them identical.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
