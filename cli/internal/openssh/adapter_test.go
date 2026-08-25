//go:build darwin || linux

package openssh

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestNewRejectsSharedWritableControlDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{Executable: "/usr/bin/ssh", ControlDirectory: directory})
	if err == nil || !strings.Contains(err.Error(), "must not be writable by other users") {
		t.Fatalf("error = %v", err)
	}
}

func TestCloseHonorsCanceledContext(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	command := exec.Command("/bin/sh", "-c", `
trap '' TERM
: > "$1"
while :; do sleep 3600; done
`, "stubborn-master", readyPath)
	configureProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	master := &sshMaster{command: command, done: make(chan struct{})}
	go master.wait()
	t.Cleanup(func() {
		select {
		case <-master.done:
		default:
			_ = killProcess(master.command)
			<-master.done
		}
	})
	waitForFile(t, readyPath)

	adapter := &Adapter{
		waitDelay: 50 * time.Millisecond,
		masters:   map[core.HostAlias]*sshMaster{"dev": master},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapter.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v, want context.Canceled", err)
	}
	select {
	case <-master.done:
	case <-time.After(2 * time.Second):
		t.Fatal("master survived background cleanup")
	}
}

func TestObserveReusesMasterWithoutAliasValidation(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.masters["dev"] = &sshMaster{done: make(chan struct{})}

	err := adapter.Observe(context.Background(), "dev", func([]core.Listener) {})
	if core.ErrorDiagnostic(err) != "transport_unavailable" {
		t.Fatalf("Observe error = %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	if len(lines) != 1 || strings.Contains(lines[0], "-G") {
		t.Fatalf("commands = %q, want one discovery command", lines)
	}
}

func TestEnsureMasterValidatesAliasBeforeStarting(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "exit 1\n")

	_, err := adapter.ensureMaster(context.Background(), "dev")
	if core.ErrorDiagnostic(err) != "invalid_alias" {
		t.Fatalf("ensureMaster error = %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if command := strings.TrimSpace(string(commands)); command != "-G dev" {
		t.Fatalf("command = %q, want %q", command, "-G dev")
	}
}

func newLoggingAdapter(t *testing.T, scriptSuffix string) (*Adapter, string) {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands")
	executable := filepath.Join(directory, "ssh")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_FORWARD_TEST_LOG"
` + scriptSuffix
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Adapter{
		executable:       executable,
		controlDirectory: directory,
		waitDelay:        time.Second,
		environment:      append(approvedEnvironment(), "SSH_FORWARD_TEST_LOG="+logPath),
		masters:          make(map[core.HostAlias]*sshMaster),
	}, logPath
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
