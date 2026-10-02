//go:build darwin || linux

package openssh

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestForwardUncertainInstallationRetiresMaster(t *testing.T) {
	for _, direction := range []core.ForwardDirection{core.RemoteToLocal, core.LocalToRemote} {
		for _, tc := range []struct {
			name, action string
			cancelCaller bool
		}{
			{"control timeout", "exec sleep 30", false},
			{"caller cancellation", "exec sleep 30", true},
			{"rejection text before timeout", "printf 'cannot listen to port\\n' >&2; exec sleep 30", false},
			{"rejection text before signal", "printf 'cannot listen to port\\n' >&2; kill -KILL $$", false},
			{"lost confirmation", "printf 'read from master failed\\n' >&2; exit 1", false},
			{"zero exit with lost confirmation", "printf 'read from master failed\\n' >&2; exit 0", false},
		} {
			t.Run(string(direction)+"/"+tc.name, func(t *testing.T) {
				adapter, logPath := newLoggingAdapter(t, `
case " $* " in
*" -O forward "*) : > installed; `+tc.action+` ;;
*" -O check "*) exit 0 ;;
esac
`)
				adapter.controlTimeout, adapter.waitDelay = time.Second, 50*time.Millisecond
				master := newTestMaster(t, true)
				adapter.master = master
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				ready := false
				go func() {
					result <- adapter.Forward(ctx, core.ForwardTarget{Direction: direction, LocalPort: 15173, RemotePort: 5173}, func() { ready = true })
				}()
				// Cancel only after the stub has committed the installation.
				require.Eventually(t, func() bool {
					_, err := os.Stat(filepath.Join(adapter.controlDirectory, "installed"))
					return err == nil
				}, 5*time.Second, 5*time.Millisecond)
				if tc.cancelCaller {
					cancel()
				}
				select {
				case err := <-result:
					require.Equal(t, "transport_unavailable", core.ErrorDiagnostic(err), "uncertain installation must not trigger port fallback")
				case <-time.After(3 * time.Second):
					t.Fatal("uncertain installation did not finish cleanup")
				}
				require.False(t, ready)
				select {
				case <-master.done:
				default:
					t.Fatal("uncertain installation left its master running")
				}
				commands := readCommands(t, logPath)
				require.NotContains(t, commands, "-O cancel", "do not cancel a tuple whose ownership is uncertain")
				require.NotContains(t, commands, "-O check", "master liveness cannot establish installation rejection")
			})
		}
	}
}

func TestForwardNotStartedPreservesMaster(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	master := newTestMaster(t, false)
	adapter.master = master
	adapter.executable = filepath.Join(t.TempDir(), "missing-ssh")
	err := adapter.Forward(t.Context(), core.ForwardTarget{Direction: core.RemoteToLocal, LocalPort: 15173, RemotePort: 5173}, func() { t.Error("unstarted forward became ready") })
	require.Equal(t, "transport_unavailable", core.ErrorDiagnostic(err))
	_, logErr := os.Stat(logPath)
	require.True(t, os.IsNotExist(logErr), "an unstarted request must not be reclassified through a health check")
	select {
	case <-master.done:
		t.Fatal("a command that never started retired its master")
	default:
	}
}

func TestCancelForwardChecksWarningsEvenWithZeroExit(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		stopMaster    bool
	}{
		{"absent tuple", "mux_client_forward: forwarding request failed: port not forwarded", false},
		{"lost confirmation", "read from master failed", true},
		{"refused cancellation", "mux_client_forward: forwarding request failed: cancellation refused", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, _ := newLoggingAdapter(t, `
case " $* " in
*" -O cancel "*) printf '%s\n' '`+tc.message+`' >&2; exit 0 ;;
esac
`)
			adapter.waitDelay = time.Second
			master := newTestMaster(t, false)
			adapter.master = master
			adapter.cancelForward(master, controlForward{flag: "-L", spec: "0.0.0.0:15173:127.0.0.1:5173"})
			select {
			case <-master.done:
				require.True(t, tc.stopMaster, "an explicitly absent tuple should not retire the master")
			default:
				require.False(t, tc.stopMaster, "unconfirmed cancellation must retire the master")
			}
		})
	}
}
