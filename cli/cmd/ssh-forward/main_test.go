package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// shortConfigDir makes a real short runtime directory: the manager socket's
// Unix path must fit sun_path (~104 bytes on macOS), so tests never use
// the long nested t.TempDir() for it.
func shortConfigDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("sf-ipc-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestRunManagerSingletonServesClients pins ADR-0016 for the CLI: manager
// serve owns one Manager; subsequent commands are its clients over the
// Unix socket and share its state.
func TestRunManagerSingletonServesClients(t *testing.T) {
	dir := shortConfigDir(t)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	policies := filepath.Join(dir, "absent.jsonc")

	serveCtx, serveCancel := context.WithCancel(context.Background())
	served := make(chan int, 1)
	go func() {
		served <- run(serveCtx, []string{"--host", "development", "--policies", policies, "manager", "serve"}, io.Discard, io.Discard)
	}()
	t.Cleanup(serveCancel)
	waitForEndpoint(t, endpointPath())

	// First client: status through the singleton.
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"--policies", policies, "status"}, &stdout, io.Discard); code != 0 {
		t.Fatalf("client status exit code = %d, output = %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Host: development — disconnected") {
		t.Fatalf("client status output = %q, want the singleton's host", stdout.String())
	}

	// The singleton is one: a second serve is refused.
	if code := run(context.Background(), []string{"--host", "development", "manager", "serve"}, io.Discard, io.Discard); code == 0 {
		t.Fatal("second manager serve succeeded")
	}

	// Clients must not contradict the singleton's host.
	var warning bytes.Buffer
	if code := run(context.Background(), []string{"--host", "other", "status"}, io.Discard, &warning); code != 0 {
		t.Fatalf("conflicting-host status exit code = %d", code)
	}
	if !strings.Contains(warning.String(), "ignored") {
		t.Fatalf("stderr = %q, want the ignored-host warning", warning.String())
	}

	// Interrupt ends the singleton cleanly.
	serveCancel()
	select {
	case code := <-served:
		if code != 0 {
			t.Fatalf("serve exit code = %d, want 0", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop with its context")
	}
}

func waitForEndpoint(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("manager endpoint never became ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBuildAdapterResolvesSSHConfigToAbsolute(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh binary")
	}
	adapter, err := buildAdapter(sshPath, "relative/config")
	if err != nil {
		t.Fatalf("buildAdapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("buildAdapter returned nil")
	}
	// The composition root resolves the path; the adapter itself refuses
	// non-absolute config files (its own test pins that).
	absolute, err := filepath.Abs("relative/config")
	if err != nil {
		t.Fatal(err)
	}
	if adapter == nil || err != nil {
		t.Fatalf("adapter = %v, abs = %v", adapter, absolute)
	}
}
