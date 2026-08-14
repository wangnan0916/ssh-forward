//go:build integration

package sshhost_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type capabilities struct {
	UID     int  `json:"uid"`
	ProcTCP bool `json:"proc_tcp"`
	SS      bool `json:"ss"`
	LSOF    bool `json:"lsof"`
	Python  bool `json:"python"`
}

func TestDisposableHostProvidesUnprivilegedDiscoveryEnvironment(t *testing.T) {
	config := isolatedSSHConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/ssh", "-F", config, "ssh-forward-test-host", "sh", "-s")
	command.Stdin = bytes.NewBufferString(`
has() { command -v "$1" >/dev/null 2>&1 && printf true || printf false; }
printf '{"uid":%s,"proc_tcp":%s,"ss":%s,"lsof":%s,"python":%s}\n' \
    "$(id -u)" "$([ -r /proc/net/tcp ] && printf true || printf false)" \
    "$(has ss)" "$(has lsof)" "$(has python3)"
`)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("inspect disposable SSH host: %v", err)
	}
	var got capabilities
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode host capabilities %q: %v", output, err)
	}
	if got.UID == 0 {
		t.Fatal("integration SSH user is root")
	}
	if !got.ProcTCP || !got.SS || !got.LSOF || !got.Python {
		t.Fatalf("incomplete discovery environment: %+v", got)
	}
}

func isolatedSSHConfig(t *testing.T) string {
	t.Helper()
	runID := os.Getenv("SSH_FORWARD_TEST_RUN_ID")
	config := os.Getenv("SSH_FORWARD_TEST_SSH_CONFIG")
	if runID == "" || config == "" {
		t.Fatal("integration harness environment is required; run through scripts/test-integration")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
	expected := filepath.Join(repositoryRoot, ".tmp", "integration", runID, "ssh_config")
	resolvedConfig, err := filepath.EvalSymlinks(config)
	if err != nil {
		t.Fatalf("resolve integration SSH config: %v", err)
	}
	resolvedExpected, err := filepath.EvalSymlinks(expected)
	if err != nil {
		t.Fatalf("resolve expected integration SSH config: %v", err)
	}
	if resolvedConfig != resolvedExpected {
		t.Fatalf("refusing SSH config outside disposable harness: %s", resolvedConfig)
	}

	command := exec.Command("/usr/bin/ssh", "-G", "-F", resolvedConfig, "ssh-forward-test-host")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve disposable SSH configuration: %v", err)
	}
	effective := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), " ")
		if _, exists := effective[key]; ok && !exists {
			effective[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(effective["port"])
	if err != nil || port < 1 || port == 22 {
		t.Fatalf("unsafe disposable SSH port %q", effective["port"])
	}
	expectedIdentity := filepath.Join(filepath.Dir(resolvedConfig), "id_ed25519")
	if effective["hostname"] != "127.0.0.1" || effective["user"] != "testdev" || effective["identityfile"] != expectedIdentity {
		t.Fatalf("unsafe effective SSH configuration: hostname=%q user=%q identity=%q", effective["hostname"], effective["user"], effective["identityfile"])
	}
	return resolvedConfig
}
