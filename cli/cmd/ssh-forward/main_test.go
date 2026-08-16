package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// run() with no host or no command must fail before any Manager is built,
// so these tests never touch the network or the configured Development
// Host; the command surface itself is tested in cli/internal/cli.
func TestRunRequiresHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status"}, &stdout, &stderr); code == 0 {
		t.Fatal("run without --host succeeded")
	}
	if !strings.Contains(stderr.String(), "--host is required") {
		t.Fatalf("stderr = %q, want missing-host message", stderr.String())
	}
}

func TestRunRequiresCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--host", "development"}, &stdout, &stderr); code == 0 {
		t.Fatal("run without a command succeeded")
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

// TestRunStatusWithoutConnection builds the Manager but only reads its
// Snapshot: the actor connects lazily on the first command, so a status
// read never dials the Development Host.
func TestRunStatusWithoutConnection(t *testing.T) {
	var stdout, stderr bytes.Buffer
	policies := filepath.Join(t.TempDir(), "absent.jsonc")
	code := run([]string{"--host", "development", "--policies", policies, "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, stderr = %s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Host: development — disconnected") {
		t.Fatalf("status output = %q", output)
	}
	if !strings.Contains(output, "Discovery: stopped") {
		t.Fatalf("status output missing discovery state: %q", output)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--host", "development", "frobnicate"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown command exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
