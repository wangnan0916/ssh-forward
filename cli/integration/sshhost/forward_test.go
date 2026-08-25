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

func TestPrivateMasterReusesAliasWithoutConfiguredForwards(t *testing.T) {
	environment := loadTestEnvironment(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = environment.adapter.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	emitted := false
	err := environment.adapter.Observe(ctx, core.HostAlias(environment.host), func(listeners []core.Listener) {
		emitted = len(listeners) != 0
		cancel()
	})
	if !emitted {
		t.Fatalf("private master discovery did not reuse the configured Host alias: %v", err)
	}
	configuredLocal := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_CONFIG_LOCAL", 38086)
	connection, dialErr := net.DialTimeout(
		"tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(configuredLocal))),
		50*time.Millisecond,
	)
	if dialErr == nil {
		_ = connection.Close()
		t.Fatalf("configured LocalForward unexpectedly opened port %d", configuredLocal)
	}
	configuredRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_CONFIG_REMOTE", 38087)
	wantRemotePortClosed(t, environment, configuredRemote)
}

func TestUpgradeCleansLegacyMasterBeforeRebinding(t *testing.T) {
	environment := loadTestEnvironment(t)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	localPort := availableLocalPort(t)
	legacyDone := startLegacyForward(t, environment, localPort, remotePort)
	wantForwardedEcho(t, localPort, "legacy-forward")

	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{
			RemotePort: remotePort, LocalPort: localPort,
		}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive
	})
	select {
	case <-legacyDone:
	default:
		t.Fatal("replacement Forward became active before the legacy master exited")
	}
	wantForwardedEcho(t, localPort, "replacement-forward")
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

func TestStrictPreferredLocalPortReportsConflict(t *testing.T) {
	environment := loadTestEnvironment(t)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	preferredPort, blocker := occupiedLocalPort(t)
	t.Cleanup(func() { _ = blocker.Close() })
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{
			RemotePort: remotePort, LocalPort: preferredPort,
		}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 &&
			status.Forwards[0].State == core.ForwardFailed &&
			status.Forwards[0].Diagnostic == "local_port_conflict"
	})
	forward := status.Forwards[0]
	if forward.PreferredLocalPort != preferredPort || forward.LocalPort != preferredPort {
		t.Fatalf("forward status = %#v", forward)
	}
}

func TestPublishesLocalServiceOnRemoteLoopback(t *testing.T) {
	environment := loadTestEnvironment(t)
	localPort := startLocalEchoServer(t)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE", 38085)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		PublishedForwards: []core.PublishedForward{{LocalPort: localPort, RemotePort: remotePort}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 &&
			status.Forwards[0].Direction == core.LocalToRemote &&
			status.Forwards[0].State == core.ForwardActive
	})
	forward := status.Forwards[0]
	if forward.LocalPort != localPort || forward.RemotePort != remotePort {
		t.Fatalf("published forward = %#v", forward)
	}
	if _, found := listenersByPort(status.Listeners)[remotePort]; found {
		t.Fatalf("published port %d leaked into discovered listeners", remotePort)
	}

	wantPublishedEcho(t, environment, remotePort, "published-echo")
	wantRemoteLoopbackListener(t, environment, remotePort)
}

func TestPublishedForwardSurvivesLocalTargetRestart(t *testing.T) {
	environment := loadTestEnvironment(t)
	localPort := availableLocalPort(t)
	_, stopLocal := startLocalEchoServerOnPort(t, localPort)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE", 38085)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		PublishedForwards: []core.PublishedForward{{LocalPort: localPort, RemotePort: remotePort}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive
	})
	wantPublishedEcho(t, environment, remotePort, "before-restart")
	stopLocal()
	wantRemoteLoopbackListener(t, environment, remotePort)
	_, _ = startLocalEchoServerOnPort(t, localPort)
	wantPublishedEcho(t, environment, remotePort, "after-restart")
}

func TestPublishedForwardReportsOccupiedRemotePort(t *testing.T) {
	environment := loadTestEnvironment(t)
	localPort := startLocalEchoServer(t)
	occupiedRemotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		PublishedForwards: []core.PublishedForward{{
			LocalPort: localPort, RemotePort: occupiedRemotePort,
		}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 &&
			status.Forwards[0].State == core.ForwardFailed &&
			status.Forwards[0].Diagnostic == "remote_port_unavailable"
	})
	if status.Forwards[0].Direction != core.LocalToRemote {
		t.Fatalf("published forward = %#v", status.Forwards[0])
	}
}

func TestPublishedForwardRejectsGatewayPortsWildcardBind(t *testing.T) {
	environment := loadTestEnvironment(t)
	environment.host = environment.unsafeHost
	localPort := availableLocalPort(t)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE", 38085)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		PublishedForwards: []core.PublishedForward{{LocalPort: localPort, RemotePort: remotePort}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	status := waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 1 &&
			status.Forwards[0].State == core.ForwardFailed &&
			status.Forwards[0].Diagnostic == "remote_bind_not_loopback"
	})
	if status.Forwards[0].Direction != core.LocalToRemote {
		t.Fatalf("published forward = %#v", status.Forwards[0])
	}
	output, err := runRemoteScript(environment, fmt.Sprintf(
		"/usr/bin/ss -H -ltn 'sport = :%d'\n", remotePort,
	))
	if err != nil || strings.TrimSpace(output) != "" {
		t.Fatalf("unsafe published listener survived cancellation: %q, %v", output, err)
	}
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

func TestCancelingPublishedForwardKeepsSharedForwardsAlive(t *testing.T) {
	environment := loadTestEnvironment(t)
	importedRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	importedLocal := availableLocalPort(t)
	firstLocal := startLocalEchoServer(t)
	secondLocal := startLocalEchoServer(t)
	firstRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE", 38085)
	secondRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE_SECOND", 38088)
	remembered := core.RememberedForward{RemotePort: importedRemote, LocalPort: importedLocal}
	firstPublished := core.PublishedForward{LocalPort: firstLocal, RemotePort: firstRemote}
	secondPublished := core.PublishedForward{LocalPort: secondLocal, RemotePort: secondRemote}
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{remembered},
		PublishedForwards:  []core.PublishedForward{firstPublished, secondPublished},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 3 && allForwardsActive(status.Forwards)
	})
	wantForwardedEcho(t, importedLocal, "imported-before-cancel")
	wantPublishedEcho(t, environment, firstRemote, "first-published")
	wantPublishedEcho(t, environment, secondRemote, "second-published")

	if err := manager.UpdateIntent(context.Background(), core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{remembered},
		PublishedForwards:  []core.PublishedForward{secondPublished},
	}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status core.Status) bool {
		return len(status.Forwards) == 2 && allForwardsActive(status.Forwards)
	})
	wantRemotePortClosed(t, environment, firstRemote)
	wantForwardedEcho(t, importedLocal, "imported-after-cancel")
	wantPublishedEcho(t, environment, secondRemote, "published-after-cancel")
}

func TestDesiredForwardsRecoverAfterMasterExit(t *testing.T) {
	environment := loadTestEnvironment(t)
	importedRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	importedLocal := availableLocalPort(t)
	localService := startLocalEchoServer(t)
	publishedRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE", 38085)
	manager := core.NewManager(core.HostAlias(environment.host), environment.adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{
			RemotePort: importedRemote, LocalPort: importedLocal,
		}},
		PublishedForwards: []core.PublishedForward{{
			LocalPort: localService, RemotePort: publishedRemote,
		}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	waitForStatus(t, manager, func(status core.Status) bool {
		return status.Discovery.State == core.DiscoveryActive &&
			len(status.Forwards) == 2 && allForwardsActive(status.Forwards)
	})
	wantForwardedEcho(t, importedLocal, "imported-before-recovery")
	wantPublishedEcho(t, environment, publishedRemote, "published-before-recovery")
	exitProductMaster(t, environment)
	waitForStatus(t, manager, func(status core.Status) bool {
		return status.Discovery.State != core.DiscoveryActive || !allForwardsActive(status.Forwards)
	})
	waitForStatus(t, manager, func(status core.Status) bool {
		return status.Discovery.State == core.DiscoveryActive &&
			len(status.Forwards) == 2 && allForwardsActive(status.Forwards)
	})
	wantForwardedEcho(t, importedLocal, "imported-after-recovery")
	wantPublishedEcho(t, environment, publishedRemote, "published-after-recovery")
}

type testEnvironment struct {
	ssh              string
	config           string
	host             string
	unsafeHost       string
	controlDirectory string
	adapter          *openssh.Adapter
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
	controlDirectory := t.TempDir()
	adapter, err := openssh.New(openssh.Options{
		Executable: ssh, ConfigFile: config, ControlDirectory: controlDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	host := os.Getenv("SSH_FORWARD_TEST_HOST_ALIAS")
	if host == "" {
		host = "ssh-forward-test-host"
	}
	unsafeHost := os.Getenv("SSH_FORWARD_TEST_UNSAFE_HOST_ALIAS")
	if unsafeHost == "" {
		unsafeHost = "ssh-forward-test-host-gatewayports-yes"
	}
	return testEnvironment{
		ssh: ssh, config: config, host: host, unsafeHost: unsafeHost,
		controlDirectory: controlDirectory, adapter: adapter,
	}
}

func runRemoteScript(environment testEnvironment, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx, environment.ssh, "-F", environment.config,
		"-o", "ClearAllForwardings=yes", environment.host, "sh", "-s",
	)
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	return string(output), err
}

func wantPublishedEcho(
	t *testing.T,
	environment testEnvironment,
	remotePort uint16,
	message string,
) {
	t.Helper()
	script := fmt.Sprintf(
		"printf '%%s' '%s' | /usr/bin/socat - TCP4:127.0.0.1:%d\n",
		message, remotePort,
	)
	output, err := runRemoteScript(environment, script)
	if err != nil || output != message {
		t.Fatalf("remote published echo = %q, %v; want %q", output, err, message)
	}
}

func wantRemoteLoopbackListener(t *testing.T, environment testEnvironment, port uint16) {
	t.Helper()
	output, err := runRemoteScript(environment, fmt.Sprintf(
		"/usr/bin/ss -H -ltn 'sport = :%d'\n", port,
	))
	if err != nil || !strings.Contains(output, "127.0.0.1:"+strconv.Itoa(int(port))) {
		t.Fatalf("remote listener is not loopback-only: %q, %v", output, err)
	}
}

func wantRemotePortClosed(t *testing.T, environment testEnvironment, port uint16) {
	t.Helper()
	output, err := runRemoteScript(environment, fmt.Sprintf(
		"/usr/bin/ss -H -ltn 'sport = :%d'\n", port,
	))
	if err != nil || strings.TrimSpace(output) != "" {
		t.Fatalf("remote port %d remains open: %q, %v", port, output, err)
	}
}

func exitProductMaster(t *testing.T, environment testEnvironment) {
	t.Helper()
	entries, err := os.ReadDir(environment.controlDirectory)
	if err != nil {
		t.Fatal(err)
	}
	controlPath := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "master-") {
			if controlPath != "" {
				t.Fatalf("multiple product master sockets in %s", environment.controlDirectory)
			}
			controlPath = entry.Name()
		}
	}
	if controlPath == "" {
		t.Fatalf("product master socket not found in %s", environment.controlDirectory)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx, environment.ssh, "-F", "/dev/null", "-S", controlPath,
		"-O", "exit", environment.host,
	)
	command.Dir = environment.controlDirectory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("exit product master: %v: %s", err, output)
	}
}

func startLegacyForward(
	t *testing.T,
	environment testEnvironment,
	localPort uint16,
	remotePort uint16,
) <-chan struct{} {
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
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = command.Process.Kill()
			<-done
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := runLegacyControl(environment, "check", ""); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("legacy master exited before becoming ready")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("legacy master did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	if err := runLegacyControl(environment, "forward", forward); err != nil {
		t.Fatalf("start legacy Forward: %v", err)
	}
	return done
}

func runLegacyControl(environment testEnvironment, operation, forward string) error {
	arguments := []string{
		"-F", environment.config, "-S", "master-%C", "-O", operation,
	}
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

func allForwardsActive(forwards []core.ForwardStatus) bool {
	if len(forwards) == 0 {
		return false
	}
	for _, forward := range forwards {
		if forward.State != core.ForwardActive {
			return false
		}
	}
	return true
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

func startLocalEchoServer(t *testing.T) uint16 {
	port, _ := startLocalEchoServerOnPort(t, 0)
	return port
}

func startLocalEchoServerOnPort(t *testing.T, port uint16) (uint16, func()) {
	t.Helper()
	listener, err := net.Listen(
		"tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))),
	)
	if err != nil {
		t.Fatal(err)
	}
	stop := func() { _ = listener.Close() }
	t.Cleanup(stop)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port), stop
}

func listenersByPort(listeners []core.Listener) map[uint16]core.Listener {
	indexed := make(map[uint16]core.Listener, len(listeners))
	for _, listener := range listeners {
		indexed[listener.Port] = listener
	}
	return indexed
}
