//go:build integration

package sshhost

import (
	"context"
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
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, environment.ssh, "-F", "/dev/null", "-S", controlPath, "-O", "exit", environment.host)
	command.Dir = environment.controlDirectory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("exit product master: %v: %s", err, output)
	}
}
