//go:build darwin || linux

package openssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/diagnostics"
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
	var backend *core.BackendError
	require.ErrorAs(t, err, &backend)
	require.Equal(t, "invalid_alias", backend.Diagnostic)
	require.NotContains(t, readCommands(t, logPath), "-M")
}

func TestEnsureMasterMissingSSHConfig(t *testing.T) {
	for _, source := range []string{"config file", "connection arguments", "relative path", "last argument missing"} {
		t.Run(source, func(t *testing.T) {
			adapter, logPath := newLoggingAdapter(t, "")
			missing := filepath.Join(adapter.controlDirectory, "deleted-config")
			switch source {
			case "connection arguments":
				adapter.connectionArguments = []string{"-F", missing}
			case "relative path":
				adapter.configFile = "deleted-config"
			case "last argument missing":
				adapter.connectionArguments = []string{"-F", "/dev/null", "-F", missing}
			default:
				adapter.configFile = missing
			}

			master, err := adapter.ensureMaster(t.Context())
			require.Nil(t, master)
			var backend *core.BackendError
			require.ErrorAs(t, err, &backend)
			require.Equal(t, "ssh_config_missing", backend.Diagnostic)
			require.NotContains(t, err.Error(), missing)
			_, err = os.Stat(logPath)
			require.ErrorIs(t, err, os.ErrNotExist, "must not invoke SSH, including control exit or master start")
		})
	}
	require.Contains(t, diagnostics.Text("ssh_config_missing"), "config file")
	detail, fix := diagnostics.DoctorAdvice("ssh_config_missing", "dev")
	require.Contains(t, detail, "-F")
	require.Contains(t, fix, "Restore")
	require.Contains(t, fix, "host connection settings")
}

func TestValidateAliasSSHConfig(t *testing.T) {
	for _, name := range []string{"existing", "none", "dev null", "arguments override", "last config wins", "last none wins", "other stat error"} {
		t.Run(name, func(t *testing.T) {
			adapter, logPath := newLoggingAdapter(t, "")
			config := filepath.Join(adapter.controlDirectory, "config")
			require.NoError(t, os.WriteFile(config, []byte("Host dev\n"), 0o600))
			adapter.configFile = config
			switch name {
			case "none":
				adapter.configFile = "none"
			case "dev null":
				adapter.configFile = "/dev/null"
			case "arguments override":
				adapter.configFile = filepath.Join(adapter.controlDirectory, "missing")
				adapter.connectionArguments = []string{"-F", config}
			case "last config wins":
				adapter.connectionArguments = []string{"-F", filepath.Join(adapter.controlDirectory, "missing"), "-F", config}
			case "last none wins":
				adapter.connectionArguments = []string{"-F", filepath.Join(adapter.controlDirectory, "missing"), "-F", "none"}
			case "other stat error":
				// ENOTDIR is deterministic even in root CI, unlike permissions.
				adapter.configFile = filepath.Join(config, "child")
				_, err := os.Stat(adapter.configFile)
				require.Error(t, err)
				require.False(t, errors.Is(err, os.ErrNotExist))
			}
			require.NoError(t, adapter.validateAlias(t.Context(), "dev"))
			require.Contains(t, readCommands(t, logPath), "-G dev")
		})
	}
}

func TestEnsureMasterExistingConfigValidationFailure(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "printf 'Permission denied\\n' >&2; exit 1\n")
	adapter.configFile = filepath.Join(adapter.controlDirectory, "config")
	require.NoError(t, os.WriteFile(adapter.configFile, nil, 0o600))
	_, err := adapter.ensureMaster(t.Context())
	var backend *core.BackendError
	require.ErrorAs(t, err, &backend)
	require.Equal(t, "invalid_alias", backend.Diagnostic)
	require.NotContains(t, readCommands(t, logPath), "-M")
}

func TestEnsureMasterMissingConfigPreservesCancellationAndExistingMaster(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.configFile = filepath.Join(adapter.controlDirectory, "missing")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := adapter.ensureMaster(ctx)
	require.ErrorIs(t, err, context.Canceled)

	master := &sshMaster{done: make(chan struct{})}
	adapter.master = master
	got, err := adapter.ensureMaster(t.Context())
	require.NoError(t, err)
	require.Same(t, master, got)
	select {
	case <-master.done:
		t.Fatal("missing config stopped existing master")
	default:
	}
	_, err = os.Stat(logPath)
	require.ErrorIs(t, err, os.ErrNotExist)
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
