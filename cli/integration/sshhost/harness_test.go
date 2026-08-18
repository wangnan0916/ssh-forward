//go:build integration

package sshhost_test

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

func testHostConnector(t *testing.T) core.HostConnector {
	t.Helper()
	adapter, err := openssh.New(openssh.Options{
		Executable:   "/usr/bin/ssh",
		ConfigFile:   isolatedSSHConfig(t),
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("create OpenSSH Adapter: %v", err)
	}
	return adapter
}

func testConfiguredManager(t *testing.T, connector core.HostConnector, policySources ...func() ([]core.ForwardingPolicy, string)) core.Manager {
	t.Helper()
	return core.NewConfiguredManager(core.HostAlias(testHostAlias()), connector, proxy.NewAllocator, policySources...)
}

// Harness identity, declared once in scripts/internal/harness.env and
// exported by scripts/test-integration. The defaults here keep the Go side
// in step when the environment is not exported, so the tests can also run
// against a manually provisioned host.
const (
	fixturePortV4Default = 38080
	fixturePortV6Default = 38081
	testHostAliasDefault = "ssh-forward-test-host"
	testUserDefault      = "testdev"
)

func harnessValue(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func harnessPort(key string, fallback int) uint16 {
	if raw := os.Getenv(key); raw != "" {
		if port, err := strconv.Atoi(raw); err == nil && port > 0 && port < 65536 {
			return uint16(port)
		}
	}
	return uint16(fallback)
}

func fixturePortV4() uint16 { return harnessPort("SSH_FORWARD_FIXTURE_PORT_V4", fixturePortV4Default) }

func fixturePortV6() uint16 { return harnessPort("SSH_FORWARD_FIXTURE_PORT_V6", fixturePortV6Default) }

func testHostAlias() string { return harnessValue("SSH_FORWARD_TEST_HOST_ALIAS", testHostAliasDefault) }

func testUser() string { return harnessValue("SSH_FORWARD_TEST_USER", testUserDefault) }
