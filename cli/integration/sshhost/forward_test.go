//go:build integration

package sshhost

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

func TestDiscoversReachableListenersAndForwardsRemotePort(t *testing.T) {
	config := os.Getenv("SSH_FORWARD_TEST_SSH_CONFIG")
	if config == "" {
		t.Fatal("SSH_FORWARD_TEST_SSH_CONFIG is not set; run scripts/test-integration")
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: ssh, ConfigFile: config})
	if err != nil {
		t.Fatal(err)
	}
	host := os.Getenv("SSH_FORWARD_TEST_HOST_ALIAS")
	if host == "" {
		host = "ssh-forward-test-host"
	}
	port := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	dualStackPort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_DUAL_STACK", 38082)
	ipv6OnlyPort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V6", 38081)
	manager := core.NewManager(core.HostAlias(host), adapter, []uint16{port})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		listeners := listenersByPort(status.Listeners)
		if len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive &&
			isSocat(listeners[port]) && listeners[port].WorkingDirectory != "" &&
			isSocat(listeners[dualStackPort]) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	listeners := listenersByPort(status.Listeners)
	if !isSocat(listeners[port]) || listeners[port].WorkingDirectory == "" {
		t.Fatalf("IPv4 listener metadata = %#v", listeners[port])
	}
	if !isSocat(listeners[dualStackPort]) {
		t.Fatalf("dual-stack listener was not discovered: %#v", listeners[dualStackPort])
	}
	if _, found := listeners[ipv6OnlyPort]; found {
		t.Fatalf("IPv6-only listener %d should not be reachable at 127.0.0.1", ipv6OnlyPort)
	}
	if _, found := listeners[22]; found {
		t.Fatal("the root-owned wildcard SSH listener should not be discovered")
	}

	connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		t.Fatalf("dial forwarded port: %v", err)
	}
	tcp := connection.(*net.TCPConn)
	defer tcp.Close()
	if _, err := tcp.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(tcp)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "hello" {
		t.Fatalf("reply = %q", reply)
	}
}

func isSocat(listener core.Listener) bool {
	return strings.HasPrefix(listener.App, "socat")
}

func fixturePort(t *testing.T, name string, fallback uint16) uint16 {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(parsed)
}

func listenersByPort(listeners []core.Listener) map[uint16]core.Listener {
	indexed := make(map[uint16]core.Listener, len(listeners))
	for _, listener := range listeners {
		indexed[listener.Port] = listener
	}
	return indexed
}
