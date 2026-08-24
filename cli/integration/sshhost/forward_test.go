//go:build integration

package sshhost

import (
	"context"
	"fmt"
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
	environment := loadTestEnvironment(t)
	port := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	localPort := availableLocalPort(t)
	dualStackPort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_DUAL_STACK", 38082)
	ipv6OnlyPort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V6", 38081)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{RemotePort: port, LocalPort: localPort}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := waitForStatus(t, manager, func(status core.Status) bool {
		listeners := listenersByPort(status.Listeners)
		return len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive &&
			isSocat(listeners[port]) && listeners[port].WorkingDirectory != "" &&
			isSocat(listeners[dualStackPort])
	})
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

	wantForwardedEcho(t, localPort, "hello")
}

func TestFallsBackToTemporaryLocalPortWhenPreferredPortIsBusy(t *testing.T) {
	environment := loadTestEnvironment(t)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	preferredPort, blocker := occupiedLocalPort(t)
	t.Cleanup(func() { _ = blocker.Close() })
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{
			RemotePort: remotePort, LocalPort: preferredPort, AllowFallback: true,
		}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive
	})
	forward := status.Forwards[0]
	if forward.PreferredLocalPort != preferredPort || forward.LocalPort <= preferredPort {
		t.Fatalf("forward status = %#v", forward)
	}
	wantForwardedEcho(t, forward.LocalPort, "fallback")
}

func TestAutomaticallyForwardsMatchingWorkingDirectoryWhileListenerExists(t *testing.T) {
	environment := loadTestEnvironment(t)
	user := os.Getenv("SSH_FORWARD_TEST_USER")
	if user == "" {
		user = "testdev"
	}
	port := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_AUTO", 38084)
	intent := core.ForwardingIntent{WorkingDirectoryRules: []string{"/home/" + user + "/Workspace/**"}}
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, intent)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	startScript := fmt.Sprintf(`set -eu
fixture_dir=$HOME/Workspace/project
mkdir -p "$fixture_dir"
cd "$fixture_dir"
nohup /usr/bin/socat "TCP4-LISTEN:%d,bind=127.0.0.1,reuseaddr,fork" EXEC:/bin/cat >/tmp/ssh-forward-auto.log 2>&1 </dev/null &
printf '%%s\n' "$!"
`, port)
	output, err := runRemoteScript(environment, startScript)
	if err != nil {
		t.Fatalf("start remote listener: %v: %s", err, output)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil || pid <= 0 {
		t.Fatalf("remote listener pid = %q", output)
	}
	stopScript := fmt.Sprintf("kill %d 2>/dev/null || true\n", pid)
	t.Cleanup(func() { _, _ = runRemoteScript(environment, stopScript) })

	waitForStatus(t, manager, func(status core.Status) bool {
		listeners := listenersByPort(status.Listeners)
		return len(status.Forwards) == 1 && status.Forwards[0].RemotePort == port &&
			status.Forwards[0].State == core.ForwardActive && status.Forwards[0].Automatic &&
			listeners[port].WorkingDirectory == "/home/"+user+"/Workspace/project"
	})
	wantForwardedEcho(t, port, "automatic")

	if output, err := runRemoteScript(environment, stopScript); err != nil {
		t.Fatalf("stop remote listener: %v: %s", err, output)
	}
	waitForStatus(t, manager, func(status core.Status) bool {
		if _, listening := listenersByPort(status.Listeners)[port]; !listening && len(status.Forwards) == 0 {
			connection, dialErr := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 50*time.Millisecond)
			if dialErr != nil {
				return true
			}
			_ = connection.Close()
		}
		return false
	})
}

func TestCancelingOneForwardKeepsAnotherForwardOnSharedConnection(t *testing.T) {
	environment := loadTestEnvironment(t)
	first := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	second := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_DUAL_STACK", 38082)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{
			{RemotePort: first, LocalPort: first},
			{RemotePort: second, LocalPort: second},
		},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 2 &&
			status.Forwards[0].State == core.ForwardActive &&
			status.Forwards[1].State == core.ForwardActive
	})
	wantForwardedEcho(t, first, "first")
	wantForwardedEcho(t, second, "second")

	if err := manager.UpdateIntent(context.Background(), core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{RemotePort: second, LocalPort: second}},
	}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status core.Status) bool {
		if len(status.Forwards) != 1 || status.Forwards[0].RemotePort != second ||
			status.Forwards[0].State != core.ForwardActive {
			return false
		}
		connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(first))), 50*time.Millisecond)
		if err != nil {
			return true
		}
		_ = connection.Close()
		return false
	})
	wantForwardedEcho(t, second, "still-active")
}

type testEnvironment struct {
	ssh     string
	config  string
	host    string
	adapter *openssh.Adapter
}

func loadTestEnvironment(t *testing.T) testEnvironment {
	t.Helper()
	config := os.Getenv("SSH_FORWARD_TEST_SSH_CONFIG")
	if config == "" {
		t.Fatal("SSH_FORWARD_TEST_SSH_CONFIG is not set; run scripts/test-integration")
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := openssh.New(openssh.Options{
		Executable: ssh, ConfigFile: config, ControlDirectory: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	host := os.Getenv("SSH_FORWARD_TEST_HOST_ALIAS")
	if host == "" {
		host = "ssh-forward-test-host"
	}
	return testEnvironment{ssh: ssh, config: config, host: host, adapter: adapter}
}

func runRemoteScript(environment testEnvironment, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, environment.ssh, "-F", environment.config, environment.host, "sh", "-s")
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	return string(output), err
}

func wantForwardedEcho(t *testing.T, port uint16, message string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		t.Fatalf("dial forwarded port: %v", err)
	}
	tcp := connection.(*net.TCPConn)
	defer tcp.Close()
	if _, err := tcp.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(tcp)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != message {
		t.Fatalf("reply = %q, want %q", reply, message)
	}
}

func waitForStatus(t *testing.T, manager core.Manager, condition func(core.Status) bool) core.Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var status core.Status
	for time.Now().Before(deadline) {
		var err error
		status, err = manager.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if condition(status) {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("manager status did not converge: %#v", status)
	return core.Status{}
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

func availableLocalPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func occupiedLocalPort(t *testing.T) (uint16, net.Listener) {
	t.Helper()
	for range 100 {
		blocker, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := blocker.Addr().(*net.TCPAddr).Port
		if port < 65535 {
			return uint16(port), blocker
		}
		_ = blocker.Close()
	}
	t.Fatal("could not reserve an occupied port below 65535")
	return 0, nil
}

func listenersByPort(listeners []core.Listener) map[uint16]core.Listener {
	indexed := make(map[uint16]core.Listener, len(listeners))
	for _, listener := range listeners {
		indexed[listener.Port] = listener
	}
	return indexed
}
