//go:build integration

package sshhost

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

type testEnvironment struct {
	ssh              string
	config           string
	host             string
	controlDirectory string
	adapter          *openssh.Adapter
}

func loadTestEnvironment(t *testing.T, unsafe ...bool) testEnvironment {
	t.Helper()
	config := os.Getenv("SSH_FORWARD_TEST_SSH_CONFIG")
	require.False(t, config == "", "SSH_FORWARD_TEST_SSH_CONFIG is not set; run scripts/test-integration")
	ssh, err := exec.LookPath("ssh")
	require.NoError(t, err)
	controlDirectory := t.TempDir()
	key := "SSH_FORWARD_TEST_HOST_ALIAS"
	if len(unsafe) > 0 && unsafe[0] {
		key = "SSH_FORWARD_TEST_UNSAFE_HOST_ALIAS"
	}
	host := os.Getenv(key)
	require.NotEmpty(t, host, "run scripts/test-integration")
	adapter, err := openssh.New(openssh.Options{Executable: ssh, ConfigFile: config, ControlDirectory: controlDirectory, Target: host})
	require.NoError(t, err)
	return testEnvironment{ssh: ssh, config: config, host: host, controlDirectory: controlDirectory, adapter: adapter}
}

func runRemoteScript(environment testEnvironment, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, environment.ssh, "-F", environment.config, "-o", "ClearAllForwardings=yes", environment.host, "sh", "-s")
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	return string(output), err
}

func fixturePort(t *testing.T, name string) uint16 {
	t.Helper()
	parsed, err := strconv.ParseUint(os.Getenv(name), 10, 16)
	require.NoError(t, err)
	require.NotZero(t, parsed, name)
	return uint16(parsed)
}

func (e testEnvironment) manager(t *testing.T, intent core.ForwardingIntent) core.Manager {
	t.Helper()
	manager := core.NewManager(core.HostAlias(e.host), e.adapter, intent)
	t.Cleanup(func() { require.NoError(t, manager.Close(context.Background())) })
	return manager
}
