package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run() with no host or no command must fail before any Manager is built,
// so these tests never touch the network or the configured Development
// Host; the command surface itself is tested in cli/internal/cli.
func TestRunRequiresHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &stdout, &stderr); code == 0 {
		t.Fatal("run without --host succeeded")
	}
	if !strings.Contains(stderr.String(), "no --host given") {
		t.Fatalf("stderr = %q, want missing-host message", stderr.String())
	}
}

// TestRunDefaultHostFromConfig pins the Persistent intent contract: with
// no --host, config.jsonc's default_host names the Development Host.
func TestRunDefaultHostFromConfig(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.jsonc")
	if err := os.WriteFile(configPath, []byte(`{"schema_version": 1, "default_host": "development"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_FORWARD_CONFIG_DIR", configDir)
	var stdout, stderr bytes.Buffer
	policies := filepath.Join(configDir, "absent.jsonc")
	code := run(context.Background(), []string{"--policies", policies, "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status with default host exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Host: development — disconnected") {
		t.Fatalf("status output = %q, want the config default host", stdout.String())
	}
}

// TestRunCorruptConfigDiagnosed pins the precise diagnosis for a corrupt
// config.jsonc: usage-style failure, not a silent fallback or a runtime
// error without context.
func TestRunCorruptConfigDiagnosed(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.jsonc"), []byte(`{"schema_version": 1, "default_host": 7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_FORWARD_CONFIG_DIR", configDir)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &stdout, &stderr); code != 2 {
		t.Fatalf("corrupt config exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), defaultConfigPath()) {
		t.Fatalf("stderr = %q, want the config path in the diagnosis", stderr.String())
	}
}

func TestRunRequiresCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--host", "development"}, &stdout, &stderr); code == 0 {
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
	code := run(context.Background(), []string{"--host", "development", "--policies", policies, "status"}, &stdout, &stderr)
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
	if code := run(context.Background(), []string{"--host", "development", "frobnicate"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown command exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
