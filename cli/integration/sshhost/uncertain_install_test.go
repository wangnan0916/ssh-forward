//go:build integration

package sshhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

// Install through real OpenSSH, then withhold the successful command result
// from the Adapter. This models an uncertain installation, not a dropped mux
// protocol ACK. Both directions must close before Forward returns and recover.
func TestCommittedForwardWithLostResult(t *testing.T) {
	for _, direction := range []core.ForwardDirection{core.RemoteToLocal, core.LocalToRemote} {
		t.Run(string(direction), func(t *testing.T) {
			env := loadTestEnvironment(t)
			target := core.ForwardTarget{Direction: direction}
			if direction == core.RemoteToLocal {
				target.LocalPort = availableLocalPort(t)
				target.RemotePort = fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4")
			} else {
				target.LocalPort = startLocalEchoServer(t)
				target.RemotePort = fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE")
			}
			spec := fmt.Sprintf("0.0.0.0:%d:127.0.0.1:%d", target.LocalPort, target.RemotePort)
			if direction == core.LocalToRemote {
				spec = fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", target.RemotePort, target.LocalPort)
			}
			marker := filepath.Join(env.controlDirectory, "installed")
			wrapper := filepath.Join(env.controlDirectory, "ssh-wrapper")
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
			script := `#!/bin/sh
forward=no
selected=no
previous=
for argument do
    if [ "$previous" = -O ] && [ "$argument" = forward ]; then forward=yes; fi
    if [ "$argument" = ` + quote(spec) + ` ]; then selected=yes; fi
    previous=$argument
done
if [ "$forward" = yes ] && [ "$selected" = yes ] && [ ! -e ` + quote(marker) + ` ]; then
    ` + quote(env.ssh) + ` "$@" || exit "$?"
    : > ` + quote(marker) + `
    exec sleep 30
fi
exec ` + quote(env.ssh) + ` "$@"
`
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o700))
			adapter, err := openssh.New(openssh.Options{
				Executable: wrapper, ConfigFile: env.config, Target: env.host,
				ControlDirectory: env.controlDirectory, WaitDelay: 200 * time.Millisecond,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, adapter.Close(context.Background())) })
			// An existing worker on this master must reconnect independently,
			// including while the failed request reclaims its own ports.
			env.adapter = adapter
			baselineLocal := availableLocalPort(t)
			for baselineLocal == target.LocalPort {
				baselineLocal = availableLocalPort(t)
			}
			baseline := env.manager(t, core.ForwardingIntent{RememberedForwards: []core.RememberedForward{{
				RemotePort: fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_DUAL_STACK"), LocalPort: baselineLocal,
			}}})
			baselineRecovered := func() {
				waitForStatus(t, baseline, func(status core.Status) bool {
					return allForwardsActive(status.Forwards) && localPortOpen(baselineLocal)
				})
				wantForwardedEcho(t, baselineLocal, "baseline-survives-reconnect")
			}
			baselineRecovered()
			echo := func(stage string) {
				if direction == core.RemoteToLocal {
					wantForwardedEcho(t, target.LocalPort, stage)
				} else {
					wantPublishedEcho(t, env, target.RemotePort, stage)
				}
			}
			closed := func() {
				if direction == core.RemoteToLocal {
					require.False(t, localPortOpen(target.LocalPort), "uncertain import remained reachable")
				} else {
					wantRemotePortClosed(t, env, target.RemotePort)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			ready := false
			go func() { result <- adapter.Forward(ctx, target, func() { ready = true }) }()
			require.Eventually(t, func() bool {
				_, err := os.Stat(marker)
				return err == nil
			}, 8*time.Second, 10*time.Millisecond, "real OpenSSH did not install the forward")
			echo("committed-but-unreported")
			cancel()
			select {
			case err := <-result:
				require.Equal(t, "transport_unavailable", core.ErrorDiagnostic(err))
			case <-time.After(5 * time.Second):
				t.Fatal("uncertain installation cleanup did not finish")
			}
			require.False(t, ready)
			closed()

			// The next request passes through the wrapper normally and must
			// establish a fresh master, reclaiming exactly the same ports.
			retryCtx, retryCancel := context.WithCancel(t.Context())
			defer retryCancel()
			active := make(chan struct{}, 1)
			go func() { result <- adapter.Forward(retryCtx, target, func() { active <- struct{}{} }) }()
			select {
			case <-active:
			case err := <-result:
				t.Fatalf("forward did not recover: %v", err)
			case <-time.After(15 * time.Second):
				t.Fatal("forward did not recover on a fresh master")
			}
			echo("recovered")
			baselineRecovered()
			retryCancel()
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("recovered forward cleanup did not finish")
			}
			closed()
		})
	}
}
