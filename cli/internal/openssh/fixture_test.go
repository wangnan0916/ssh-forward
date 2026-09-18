//go:build darwin || linux

package openssh

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newLoggingAdapter(t *testing.T, scriptSuffix string) (*Adapter, string) {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands")
	executable := filepath.Join(directory, "ssh")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_FORWARD_TEST_LOG"
` + scriptSuffix
	require.NoError(t, os.WriteFile(executable, []byte(script), 0o700))
	return &Adapter{
		executable:       executable,
		controlDirectory: directory,
		readyTimeout:     time.Second,
		controlTimeout:   5 * time.Second,
		waitDelay:        time.Second,
		environment:      append(approvedEnvironment(), "SSH_FORWARD_TEST_LOG="+logPath),
		target:           "dev",
	}, logPath
}

func newTestMaster(t *testing.T, ignoreTermination bool) *sshMaster {
	t.Helper()
	action := "exit 0"
	if ignoreTermination {
		action = ""
	}
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command("/bin/sh", "-c", `trap "$1" TERM; : > "$2"; while :; do sleep 3600; done`, "master", action, ready)
	configureProcess(command)
	require.NoError(t, command.Start())
	master := &sshMaster{command: command, stderr: &boundedBuffer{limit: maxStderrTailBytes}, done: make(chan struct{})}
	go master.wait()
	t.Cleanup(func() { _ = killProcess(command); <-master.done })
	require.Eventually(t, func() bool { _, err := os.Stat(ready); return err == nil }, 5*time.Second, 10*time.Millisecond)
	return master
}

func readCommands(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
