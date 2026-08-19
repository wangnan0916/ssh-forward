package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

// run() with no host or no command must fail before any Manager is built,
// so these tests never touch the network or the configured Development
// Host; the command surface itself is tested in cli/internal/cli.
func TestRunRequiresHost(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &bytes.Buffer{}, &stdout, &stderr); code == 0 {
		t.Fatal("run without --host succeeded")
	}
	if !strings.Contains(stderr.String(), "no --host given") {
		t.Fatalf("stderr = %q, want missing-host message", stderr.String())
	}
}

// TestRunDefaultHostFromConfig pins the Persistent intent contract: with
// no --host, config.jsonc's default_host names the Development Host.
func TestRunDefaultHostFromConfig(t *testing.T) {
	isolateUserEnv(t)
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.jsonc")
	if err := os.WriteFile(configPath, []byte(`{"schema_version": 1, "default_host": "development"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_FORWARD_CONFIG_DIR", configDir)
	var stdout, stderr bytes.Buffer
	policies := filepath.Join(configDir, "absent.jsonc")
	code := run(context.Background(), []string{"--policies", policies, "status"}, &bytes.Buffer{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status with default host exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Host: development") {
		t.Fatalf("status output = %q, want the config default host", stdout.String())
	}
}

// TestRunCorruptConfigDiagnosed pins the precise diagnosis for a corrupt
// config.jsonc: usage-style failure, not a silent fallback or a runtime
// error without context.
func TestRunCorruptConfigDiagnosed(t *testing.T) {
	isolateUserEnv(t)
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.jsonc"), []byte(`{"schema_version": 1, "default_host": 7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_FORWARD_CONFIG_DIR", configDir)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &bytes.Buffer{}, &stdout, &stderr); code != 2 {
		t.Fatalf("corrupt config exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), app.DefaultLayout().Config) {
		t.Fatalf("stderr = %q, want the config path in the diagnosis", stderr.String())
	}
}

func TestRunRequiresCommand(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--host", "development"}, &bytes.Buffer{}, &stdout, &stderr); code == 0 {
		t.Fatal("run without a command succeeded")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

// TestRunStatusWithoutHostConfig builds the Manager and reads its Snapshot.
// Connection starts at construction, so this only pins the configured host.
func TestRunStatusWithoutConnection(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	policies := filepath.Join(t.TempDir(), "absent.jsonc")
	code := run(context.Background(), []string{"--host", "development", "--policies", policies, "status"}, &bytes.Buffer{}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, stderr = %s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Host: development") {
		t.Fatalf("status output = %q", output)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--host", "development", "frobnicate"}, &bytes.Buffer{}, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown command exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// managerBinary is a real build of this command, produced once in
// TestMain: auto-spawn must start the product executable, never the test
// binary.
var managerBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sf-testbin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
		os.Exit(1)
	}
	managerBinary = filepath.Join(dir, "ssh-forward")
	build := exec.Command("go", "build", "-o", managerBinary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build for autospawn tests: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// isolateUserEnv keeps the in-process fallback and a hermetically empty
// SSH client configuration: auto-spawn would leave a detached manager
// owning the endpoint, and host discovery must never read the developer's
// real ~/.ssh/config.
func isolateUserEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SSH_FORWARD_NO_AUTOSPAWN", "1")
	t.Setenv("SSH_FORWARD_MANAGER_SERVE", "")
	t.Setenv("SSH_FORWARD_UI_SERVE", "")
	t.Setenv("HOME", t.TempDir())
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
	isolateUserEnv(t)
	dir := shortConfigDir(t)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	policies := filepath.Join(dir, "absent.jsonc")

	serveCtx, serveCancel := context.WithCancel(context.Background())
	served := make(chan int, 1)
	go func() {
		served <- run(serveCtx, []string{"--host", "development", "--policies", policies, "manager", "serve"}, &bytes.Buffer{}, io.Discard, io.Discard)
	}()
	t.Cleanup(serveCancel)
	waitForEndpoint(t, app.DefaultLayout().Socket)

	// First client: status through the singleton.
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"--policies", policies, "status"}, &bytes.Buffer{}, &stdout, io.Discard); code != 0 {
		t.Fatalf("client status exit code = %d, output = %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Host: development") {
		t.Fatalf("client status output = %q, want the singleton's host", stdout.String())
	}

	// The singleton is one: a second serve is refused.
	if code := run(context.Background(), []string{"--host", "development", "manager", "serve"}, &bytes.Buffer{}, io.Discard, io.Discard); code == 0 {
		t.Fatal("second manager serve succeeded")
	}

	// Clients must not contradict the singleton's host.
	var warning bytes.Buffer
	if code := run(context.Background(), []string{"--host", "other", "status"}, &bytes.Buffer{}, io.Discard, &warning); code != 0 {
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
	if err := jsonrpc.Wait(context.Background(), path, 3*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRunVersion(t *testing.T) {
	isolateUserEnv(t)
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &bytes.Buffer{}, &stdout, io.Discard); code != 0 {
		t.Fatalf("--version exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "ssh-forward 0.1.0-alpha.1") {
		t.Fatalf("--version output = %q", stdout.String())
	}
}

func TestRunHelp(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &bytes.Buffer{}, &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Available Commands:") || !strings.Contains(stdout.String(), "add") || !strings.Contains(stdout.String(), "ui") {
		t.Fatalf("--help output = %q", stdout.String())
	}
}

func TestRunAddRemembersPortWithoutHost(t *testing.T) {
	isolateUserEnv(t)
	policies := filepath.Join(t.TempDir(), "policies.jsonc")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--policies", policies, "add", "5173"}, &bytes.Buffer{}, &stdout, &stderr); code != 0 {
		t.Fatalf("add exit code = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "added port 5173\n" {
		t.Fatalf("add output = %q", stdout.String())
	}
	loaded, err := app.LoadPolicies(policies)
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "port-5173" {
		t.Fatalf("policies = %#v, want port-5173", loaded)
	}
}

func TestRunCommandHelp(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"add", "--help"}, &bytes.Buffer{}, &stdout, &stderr); code != 0 {
		t.Fatalf("add --help exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "remember a remote port") {
		t.Fatalf("add --help output = %q", stdout.String())
	}
}

// TestRunAutospawnsTheSingleton pins the zero-setup path: a command with
// no running manager starts it in the background (its own executable,
// logging next to the socket) and then executes as its client.
func TestRunAutospawnsTheSingleton(t *testing.T) {
	t.Setenv("SSH_FORWARD_MANAGER_BINARY", managerBinary)
	dir := shortConfigDir(t)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	policies := filepath.Join(dir, "absent.jsonc")

	var stdout bytes.Buffer
	code := run(context.Background(), []string{"--host", "development", "--policies", policies, "status"}, &bytes.Buffer{}, &stdout, io.Discard)
	if code != 0 {
		t.Fatalf("status with autospawn exit code = %d, output = %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Host: development") {
		t.Fatalf("status output = %q, want the spawned singleton's host", stdout.String())
	}

	// The singleton is up, recorded its pid, and answers a second command
	// without spawning anything new.
	waitForEndpoint(t, app.DefaultLayout().Socket)
	pid := readManagerPID(t, dir)

	// A second client reuses the same singleton.
	var second bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &bytes.Buffer{}, &second, io.Discard); code != 0 {
		t.Fatalf("second status exit code = %d", code)
	}
	if !strings.Contains(second.String(), "Host: development") {
		t.Fatalf("second status output = %q", second.String())
	}

	// Stop the spawned singleton cleanly (SIGTERM lets it remove its pid
	// file and socket).
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM the manager: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "manager.pid")); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager did not stop with SIGTERM")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunManagerStopAndRestart(t *testing.T) {
	t.Setenv("SSH_FORWARD_MANAGER_BINARY", managerBinary)
	dir := shortConfigDir(t)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	policies := filepath.Join(dir, "absent.jsonc")
	args := []string{"--host", "development", "--policies", policies}

	if code := run(context.Background(), append(args, "status"), &bytes.Buffer{}, io.Discard, io.Discard); code != 0 {
		t.Fatal("status autospawn failed")
	}
	firstPID := readManagerPID(t, dir)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"manager", "stop"}, &bytes.Buffer{}, &stdout, &stderr); code != 0 {
		t.Fatalf("manager stop exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "stopped\n" {
		t.Fatalf("manager stop output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"manager", "stop"}, &bytes.Buffer{}, &stdout, &stderr); code == 0 {
		t.Fatal("second manager stop succeeded")
	}
	if !strings.Contains(stderr.String(), "manager is not running") {
		t.Fatalf("second stop stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), append(args, "manager", "restart"), &bytes.Buffer{}, &stdout, &stderr); code != 0 {
		t.Fatalf("manager restart exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "restarted\n" {
		t.Fatalf("manager restart output = %q", stdout.String())
	}
	secondPID := readManagerPID(t, dir)
	if secondPID == firstPID {
		t.Fatalf("restart reused pid %d", secondPID)
	}

	stdout.Reset()
	if code := run(context.Background(), []string{"status"}, &bytes.Buffer{}, &stdout, io.Discard); code != 0 {
		t.Fatalf("status after restart exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "Host: development") {
		t.Fatalf("status after restart = %q", stdout.String())
	}

	if code := run(context.Background(), []string{"manager", "stop"}, &bytes.Buffer{}, io.Discard, io.Discard); code != 0 {
		t.Fatal("cleanup manager stop failed")
	}
}

func readManagerPID(t *testing.T, dir string) int {
	t.Helper()
	pid, err := app.ReadPIDFile(filepath.Join(dir, "manager.pid"))
	if err != nil {
		t.Fatalf("manager.pid: %v", err)
	}
	return pid
}

// TestRunHostList pins the discovery surface: hosts come from the SSH
// client configuration, the current default is marked, --json is the
// machine shape.
func TestRunHostList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu\nHost devbox\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"host", "list"}, &bytes.Buffer{}, &stdout, io.Discard); code != 0 {
		t.Fatalf("host list exit code = %d", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "ubuntu") || !strings.Contains(output, "devbox") {
		t.Fatalf("host list output = %q", output)
	}
	var jsonOut bytes.Buffer
	if code := run(context.Background(), []string{"host", "list", "--json"}, &bytes.Buffer{}, &jsonOut, io.Discard); code != 0 {
		t.Fatalf("host list --json exit code = %d", code)
	}
	if !strings.Contains(jsonOut.String(), `"ubuntu"`) || !strings.Contains(jsonOut.String(), `"devbox"`) {
		t.Fatalf("host list --json output = %q", jsonOut.String())
	}
}

// TestRunSetDefault pins the explicit default-host command: after
// ssh-forward default ALIAS, later commands resolve to it without
// prompting.
func TestRunSetDefault(t *testing.T) {
	dir := shortConfigDir(t)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	isolateUserEnv(t)
	var stdout bytes.Buffer
	if code := run(context.Background(), []string{"default", "ubuntu"}, &bytes.Buffer{}, &stdout, io.Discard); code != 0 {
		t.Fatalf("default exit code = %d, output = %s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "default host set to ubuntu") {
		t.Fatalf("default output = %q", stdout.String())
	}
	config, err := app.LoadConfig(app.DefaultLayout().Config)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.DefaultHost != "ubuntu" {
		t.Fatalf("default host = %q, want ubuntu", config.DefaultHost)
	}
}

func TestRunUINeedsSubcommand(t *testing.T) {
	isolateUserEnv(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"ui"}, &bytes.Buffer{}, &stdout, &stderr); code != 2 {
		t.Fatalf("ui exit code = %d, want 2, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "start, status, stop") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// TestRunUIStartStatusStop pins the WebUI process seam: start launches a
// background loopback server, status reprints its URL, and stop ends only
// that process.
func TestRunUIStartStatusStop(t *testing.T) {
	isolateUserEnv(t)
	t.Setenv("SSH_FORWARD_UI_NO_OPEN", "1")
	t.Setenv("SSH_FORWARD_UI_BINARY", managerBinary)
	dir := shortConfigDir(t)
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	policies := filepath.Join(dir, "policies.jsonc")
	args := []string{"--host", "development", "--policies", policies}
	invoke := func(stdout, stderr io.Writer, extra ...string) int {
		return run(context.Background(), append(append([]string{}, args...), extra...), &bytes.Buffer{}, stdout, stderr)
	}

	var stdout, stderr bytes.Buffer
	code := invoke(&stdout, &stderr, "ui", "start")
	if code != 0 {
		log, _ := os.ReadFile(filepath.Join(dir, "ui.log"))
		t.Fatalf("ui start exit = %d, stderr = %s, log = %s", code, stderr.String(), log)
	}
	url := strings.TrimSpace(stdout.String())
	t.Cleanup(func() {
		invoke(io.Discard, io.Discard, "ui", "stop")
	})
	page, query, ok := strings.Cut(url, "?")
	if !ok || !strings.HasPrefix(page, "http://127.0.0.1:") || !strings.Contains(query, "token=") {
		t.Fatalf("start url = %q", url)
	}

	snapshot, err := http.Get(strings.TrimSuffix(page, "/") + "/api/snapshot?" + query)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(snapshot.Body)
	snapshot.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"alias":"development"`)) {
		t.Fatalf("snapshot status = %d body = %s", snapshot.StatusCode, body)
	}

	missing, err := http.Get(strings.TrimSuffix(page, "/") + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", missing.StatusCode)
	}

	stdout.Reset()
	stderr.Reset()
	if code := invoke(&stdout, &stderr, "ui", "start"); code != 0 {
		t.Fatalf("second ui start exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != url {
		t.Fatalf("second start url = %q, want %q", stdout.String(), url)
	}

	stdout.Reset()
	if code := invoke(&stdout, io.Discard, "ui", "status", "--json"); code != 0 {
		t.Fatalf("ui status --json exit = %d", code)
	}
	if !strings.Contains(stdout.String(), url) {
		t.Fatalf("status --json = %q, want the live URL", stdout.String())
	}

	stdout.Reset()
	if code := invoke(&stdout, io.Discard, "ui", "stop"); code != 0 {
		t.Fatalf("ui stop exit = %d", code)
	}
	if stdout.String() != "stopped\n" {
		t.Fatalf("stop output = %q", stdout.String())
	}
	if code := invoke(io.Discard, io.Discard, "ui", "status"); code == 0 {
		t.Fatal("ui status succeeded after stop")
	}
}
