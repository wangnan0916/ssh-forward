//go:build integration

package sshhost

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

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
	active(4)
	echo("initial")
	wantForwardedEcho(t, second, "second-import")
	wantPublishedEcho(t, env, secondPublication, "second-publication")
	wantRemoteLoopbackListener(t, env, publication)

	intent.RememberedForwards = intent.RememberedForwards[:1]
	require.NoError(t, manager.UpdateIntent(t.Context(), intent))
	active(3)
	require.Eventually(t, func() bool { return !localPortOpen(second) }, 3*time.Second, 20*time.Millisecond)
	echo("after-import-removal")
	wantPublishedEcho(t, env, secondPublication, "publication-survives-import-removal")

	intent.PublishedForwards = intent.PublishedForwards[:1]
	require.NoError(t, manager.UpdateIntent(t.Context(), intent))
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
