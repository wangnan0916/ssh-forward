//go:build darwin || linux

package openssh

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestNewRejectsSharedWritableControlDirectory(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o770))
	_, err := New(Options{Executable: "/usr/bin/ssh", ControlDirectory: directory})
	require.Error(t, err)
}

func TestRemoteToLocalForwardRejectsOccupiedLoopbackPortBeforeOpenSSH(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.master = &sshMaster{done: make(chan struct{})}
	adapter.localPortAvailable = func(uint16) bool { return false }

	err := adapter.Forward(t.Context(), core.ForwardTarget{Direction: core.RemoteToLocal, LocalPort: 15173, RemotePort: 5173}, func() { t.Fatal("occupied local port became ready") })
	require.Error(t, err)
	commands, readErr := os.ReadFile(logPath)
	require.False(t, readErr != nil && !errors.Is(readErr, os.ErrNotExist), readErr)
	require.NotContains(t, string(commands), "-O forward")
}

func TestCloseHonorsCanceledContext(t *testing.T) {
	master := newTestMaster(t, true)

	adapter := &Adapter{waitDelay: 50 * time.Millisecond, master: master}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, adapter.Close(ctx), context.Canceled)
	select {
	case <-master.done:
	case <-time.After(2 * time.Second):
		t.Fatal("master survived background cleanup")
	}
}

func TestObserveReusesMasterWithoutAliasValidation(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.master = &sshMaster{done: make(chan struct{})}

	err := adapter.Observe(t.Context(), func([]core.Listener) {})
	require.Error(t, err)
	commands := readCommands(t, logPath)
	lines := strings.Split(strings.TrimSpace(commands), "\n")
	require.Falsef(t, len(lines) != 1 || strings.Contains(lines[0], "-G"), "commands = %q, want one discovery command", lines)
}

func TestEnsureMasterValidatesAliasBeforeStarting(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "exit 1\n")

	_, err := adapter.ensureMaster(t.Context())
	require.Error(t, err)
	require.NotContains(t, readCommands(t, logPath), "-M")
}

func TestForwardEndpointsAndRejectedInstallation(t *testing.T) {
	for _, tc := range []struct {
		direction     core.ForwardDirection
		local, remote uint16
	}{
		{core.RemoteToLocal, 15173, 5173},
		{core.LocalToRemote, 9222, 19222},
	} {
		t.Run(string(tc.direction), func(t *testing.T) {
			target := core.ForwardTarget{Direction: tc.direction, LocalPort: tc.local, RemotePort: tc.remote}
			adapter, logPath := newLoggingAdapter(t, `
case " $* " in
*" -O forward "*) printf 'cannot listen to port\n' >&2; exit 1 ;;
*" -O check "*) exit 0 ;;
*" -O cancel "*) exit 1 ;;
esac
`)
			adapter.master = newTestMaster(t, false)
			err := adapter.Forward(t.Context(), target, func() { t.Fatal("rejected forward became ready") })
			require.Error(t, err)
			select {
			case <-adapter.master.done:
				t.Fatal("rejected installation stopped shared master")
			default:
			}
			require.NotContains(t, readCommands(t, logPath), "-O cancel")
		})
	}
}

func TestOldForwardCleanupDoesNotCancelReplacementMaster(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	old := &sshMaster{done: make(chan struct{})}
	close(old.done)
	replacement := &sshMaster{done: make(chan struct{})}
	adapter.master = replacement
	adapter.cancelForward(old, controlForward{flag: "-L", spec: "0.0.0.0:5173:127.0.0.1:5173"})
	if commands, err := os.ReadFile(logPath); err == nil && len(commands) != 0 {
		t.Fatalf("old worker touched replacement: %s", commands)
	}
}

func TestControlCommandTimeoutDoesNotHangReconnect(t *testing.T) {
	adapter, _ := newLoggingAdapter(t, "exec sleep 30\n")
	adapter.controlTimeout = 30 * time.Millisecond
	start := time.Now()
	require.Error(t, adapter.runControl(t.Context(), "check", nil), "hung control command succeeded")
	require.False(t, time.Since(start) > time.Second, "control command exceeded timeout")
}

func TestPublishedForwardReadinessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, probe string
		cancelFails bool
	}{
		{"wildcard bind", "printf '00000000\\n'", false},
		{"probe timeout", "exec sleep 3600", false},
		{"probe failure", "printf 'awk unavailable\\n' >&2; exit 23", false},
		{"cancellation failure", "printf '00000000\\n'", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cancelScript := ""
			if tc.cancelFails {
				cancelScript = "case \" $* \" in *\" -O cancel \"*) exit 1 ;; esac\n"
			}
			script := cancelScript + "for argument do last=$argument; done\nif [ \"$last\" = 19222 ]; then " + tc.probe + "; fi\n"
			adapter, logPath := newLoggingAdapter(t, script)
			adapter.readyTimeout, adapter.waitDelay = 500*time.Millisecond, 50*time.Millisecond
			master := newTestMaster(t, false)
			adapter.master = master
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			ready := false
			err := adapter.Forward(ctx, core.ForwardTarget{Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222}, func() { ready = true })
			require.Error(t, err)
			require.NoError(t, ctx.Err(), "readiness must be bounded independently of its caller")
			require.False(t, ready, "unverified publication became ready")
			commands, err := os.ReadFile(logPath)
			require.NoError(t, err)
			require.Contains(t, string(commands), "-O cancel")
			if tc.cancelFails {
				select {
				case <-master.done:
				case <-time.After(500 * time.Millisecond):
					t.Fatal("master survived failed cancellation")
				}
			} else {
				select {
				case <-master.done:
					t.Fatal("successful cancellation stopped shared master")
				default:
				}
			}
		})
	}
}
