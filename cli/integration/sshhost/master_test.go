//go:build integration

package sshhost

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func exitProductMaster(t *testing.T, environment testEnvironment) {
	t.Helper()
	entries, err := os.ReadDir(environment.controlDirectory)
	require.NoError(t, err)
	controlPath := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "master-") {
			require.EqualValuesf(t, "", controlPath, "multiple product master sockets in %s", environment.controlDirectory)
			controlPath = entry.Name()
		}
	}
	require.Falsef(t, controlPath == "", "product master socket not found in %s", environment.controlDirectory)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, environment.ssh, "-F", "/dev/null", "-S", controlPath, "-O", "exit", environment.host)
	command.Dir = environment.controlDirectory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("exit product master: %v: %s", err, output)
	}
}

func startLegacyForward(t *testing.T, environment testEnvironment, localPort uint16, remotePort uint16) <-chan struct{} {
	t.Helper()
	command := exec.Command(
		environment.ssh, "-F", environment.config,
		"-M", "-N", "-T", "-S", "master-%C",
		"-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=yes", "-o", "ControlPersist=no",
		environment.host,
	)
	command.Dir = environment.controlDirectory
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	require.NoError(t, command.Start())
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	t.Cleanup(func() { _ = command.Process.Kill(); <-done })
	require.Eventually(t, func() bool { return runLegacyControl(environment, "check", "") == nil }, 5*time.Second, 25*time.Millisecond)

	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	require.NoError(t, runLegacyControl(environment, "forward", forward))
	return done
}

func runLegacyControl(environment testEnvironment, operation, forward string) error {
	arguments := []string{"-F", environment.config, "-S", "master-%C", "-O", operation}
	if forward != "" {
		arguments = append(arguments, "-o", "ExitOnForwardFailure=yes", "-L", forward)
	}
	arguments = append(arguments, environment.host)
	command := exec.Command(environment.ssh, arguments...)
	command.Dir = environment.controlDirectory
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}
