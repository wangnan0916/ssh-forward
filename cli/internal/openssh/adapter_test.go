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
