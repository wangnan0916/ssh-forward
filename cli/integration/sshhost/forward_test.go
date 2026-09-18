//go:build integration

package sshhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestDiscoversReachableListenersAndForwardsRemotePort(t *testing.T) {
	environment := loadTestEnvironment(t)
	port := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4")
	localPort := availableLocalPort(t)
	dualStackPort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_DUAL_STACK")
	ipv6OnlyPort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V6")
	manager := environment.manager(t, core.ForwardingIntent{RememberedForwards: []core.RememberedForward{{RemotePort: port, LocalPort: localPort}}})

	status := waitForStatus(t, manager, func(status core.Status) bool {
		listeners := listenersByPort(status.Listeners)
		return len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive &&
			isSocat(listeners[port]) && listeners[port].WorkingDirectory != "" &&
			isSocat(listeners[dualStackPort])
	})
	listeners := listenersByPort(status.Listeners)
	require.Falsef(t, !isSocat(listeners[port]) || listeners[port].WorkingDirectory == "", "IPv4 listener metadata = %#v", listeners[port])
	require.Truef(t, isSocat(listeners[dualStackPort]), "dual-stack listener was not discovered: %#v", listeners[dualStackPort])
	if _, found := listeners[ipv6OnlyPort]; found {
		t.Fatalf("IPv6-only listener %d should not be reachable at 127.0.0.1", ipv6OnlyPort)
	}
	if _, found := listeners[22]; found {
		t.Fatal("the root-owned wildcard SSH listener should not be discovered")
	}

	wantForwardedEcho(t, localPort, "hello")
	if address, found := firstNonLoopbackIPv4(t); found {
		wantForwardedEchoAt(t, address, localPort, "hello-from-non-loopback")
	} else {
		t.Log("no active non-loopback IPv4 address; external-interface assertion skipped")
	}
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
	err := environment.adapter.Observe(ctx, func(listeners []core.Listener) {
		emitted = len(listeners) != 0
		cancel()
	})
	require.Truef(t, emitted, "private master discovery did not reuse the configured Host alias: %v", err)
	configuredLocal := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_CONFIG_LOCAL")
	connection, dialErr := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(configuredLocal))), 50*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		t.Fatalf("configured LocalForward unexpectedly opened port %d", configuredLocal)
	}
	configuredRemote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_CONFIG_REMOTE")
	wantRemotePortClosed(t, environment, configuredRemote)
}

func TestUpgradeCleansLegacyMasterBeforeRebinding(t *testing.T) {
	environment := loadTestEnvironment(t)
	remotePort := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4")
	localPort := availableLocalPort(t)
	legacyDone := startLegacyForward(t, environment, localPort, remotePort)
	wantForwardedEcho(t, localPort, "legacy-forward")

	manager := environment.manager(t, core.ForwardingIntent{RememberedForwards: []core.RememberedForward{{RemotePort: remotePort, LocalPort: localPort}}})
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

func TestEndpointConflictsAndPublicationSafety(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		publish, fallback, unsafe bool
		diagnostic                string
	}{
		{name: "import fallback", fallback: true},
		{name: "strict import", diagnostic: "local_port_conflict"},
		{name: "occupied publication", publish: true, diagnostic: "remote_port_unavailable"},
		{name: "wildcard publication", publish: true, unsafe: true, diagnostic: "remote_bind_not_loopback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := loadTestEnvironment(t, tc.unsafe)
			remote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4")
			local, blocker := occupiedLocalPort(t)
			t.Cleanup(func() { _ = blocker.Close() })
			intent := core.ForwardingIntent{RememberedForwards: []core.RememberedForward{{RemotePort: remote, LocalPort: local, AllowFallback: tc.fallback}}}
			if tc.publish {
				local = startLocalEchoServer(t)
				if tc.unsafe {
					remote = fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE")
				}
				intent = core.ForwardingIntent{PublishedForwards: []core.PublishedForward{{LocalPort: local, RemotePort: remote}}}
			}
			status := waitForStatus(t, env.manager(t, intent), func(s core.Status) bool {
				if len(s.Forwards) != 1 {
					return false
				}
				f := s.Forwards[0]
				return tc.fallback && f.State == core.ForwardActive || f.State == core.ForwardFailed && f.Diagnostic == tc.diagnostic
			})
			forward := status.Forwards[0]
			switch {
			case tc.fallback:
				require.Equal(t, local, forward.PreferredLocalPort)
				require.Greater(t, forward.LocalPort, local)
				wantForwardedEcho(t, forward.LocalPort, "fallback")
			case tc.publish:
				require.Equal(t, core.LocalToRemote, forward.Direction)
				if tc.unsafe {
					wantRemotePortClosed(t, env, remote)
				}
			default:
				require.Equal(t, local, forward.LocalPort)
				require.Equal(t, local, forward.PreferredLocalPort)
			}
		})
	}
}

func TestAutomaticallyForwardsMatchingWorkingDirectoryWhileListenerExists(t *testing.T) {
	environment := loadTestEnvironment(t)
	user := os.Getenv("SSH_FORWARD_TEST_USER")
	if user == "" {
		user = "testdev"
	}
	port := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_AUTO")
	intent := core.ForwardingIntent{WorkingDirectoryRules: []string{"/home/" + user + "/Workspace/**"}}
	manager := environment.manager(t, intent)

	startScript := fmt.Sprintf(`set -eu
fixture_dir=$HOME/Workspace/project
mkdir -p "$fixture_dir"
cd "$fixture_dir"
nohup /usr/bin/socat "TCP4-LISTEN:%d,bind=127.0.0.1,reuseaddr,fork" EXEC:/bin/cat >/tmp/ssh-forward-auto.log 2>&1 </dev/null &
printf '%%s\n' "$!"
`, port)
	output, err := runRemoteScript(environment, startScript)
	require.NoErrorf(t, err, "start remote listener: %v: %s", err, output)
	pid, err := strconv.Atoi(strings.TrimSpace(output))
	require.Falsef(t, err != nil || pid <= 0, "remote listener pid = %q", output)
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

// One connection is exercised through edits, a local service restart, and an
// SSH transport restart. Every surviving forward must still carry real traffic.
func TestSharedConnectionLifecycle(t *testing.T) {
	env := loadTestEnvironment(t)
	remote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4")
	second := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_DUAL_STACK")
	local := availableLocalPort(t)
	service, stopService := startLocalEchoServerOnPort(t, availableLocalPort(t))
	publication := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE")
	secondPublication := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE_SECOND")
	intent := core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{RemotePort: remote, LocalPort: local}, {RemotePort: second, LocalPort: second}},
		PublishedForwards:  []core.PublishedForward{{LocalPort: service, RemotePort: publication}, {LocalPort: startLocalEchoServer(t), RemotePort: secondPublication}},
	}
	manager := env.manager(t, intent)
	active := func(count int) core.Status {
		return waitForStatus(t, manager, func(s core.Status) bool {
			return s.Discovery.State == core.DiscoveryActive && len(s.Forwards) == count && allForwardsActive(s.Forwards)
		})
	}
	echo := func(stage string) {
		wantForwardedEcho(t, local, "imported-"+stage)
		wantPublishedEcho(t, env, publication, "published-"+stage)
	}
	status := active(4)
	require.NotContains(t, listenersByPort(status.Listeners), publication)
	echo("initial")
	wantForwardedEcho(t, second, "second-import")
	wantPublishedEcho(t, env, secondPublication, "second-publication")
	wantRemoteLoopbackListener(t, env, publication)

	intent.RememberedForwards = intent.RememberedForwards[:1]
	require.NoError(t, manager.UpdateIntent(context.Background(), intent))
	active(3)
	require.Eventually(t, func() bool { return !localPortOpen(second) }, 3*time.Second, 20*time.Millisecond)
	echo("after-import-removal")
	wantPublishedEcho(t, env, secondPublication, "publication-survives-import-removal")

	intent.PublishedForwards = intent.PublishedForwards[:1]
	require.NoError(t, manager.UpdateIntent(context.Background(), intent))
	active(2)
	wantRemotePortClosed(t, env, secondPublication)
	echo("after-publication-removal")

	stopService()
	wantRemoteLoopbackListener(t, env, publication)
	_, _ = startLocalEchoServerOnPort(t, service)
	echo("after-local-restart")

	exitProductMaster(t, env)
	waitForStatus(t, manager, func(s core.Status) bool {
		return s.Discovery.State != core.DiscoveryActive || !allForwardsActive(s.Forwards)
	})
	active(2)
	echo("after-ssh-restart")
}
