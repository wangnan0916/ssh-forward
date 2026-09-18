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
)

func TestNewRejectsSharedWritableControlDirectory(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o770))
	_, err := New(Options{Executable: "/usr/bin/ssh", ControlDirectory: directory})
	require.Falsef(t, err == nil || !strings.Contains(err.Error(), "must not be writable by other users"), "error = %v", err)
}

func TestRemoteToLocalForwardRejectsOccupiedLoopbackPortBeforeOpenSSH(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.master = &sshMaster{done: make(chan struct{})}
	adapter.localPortAvailable = func(uint16) bool { return false }

	err := adapter.Forward(context.Background(), core.ForwardTarget{Direction: core.RemoteToLocal, LocalPort: 15173, RemotePort: 5173}, func() { t.Fatal("occupied local port became ready") })
	require.EqualValues(t, "local_port_conflict", core.ErrorDiagnostic(err))
	commands, readErr := os.ReadFile(logPath)
	require.False(t, readErr != nil && !errors.Is(readErr, os.ErrNotExist), readErr)
	require.NotContains(t, string(commands), "-O forward")
}

func TestCloseHonorsCanceledContext(t *testing.T) {
	master := newTestMaster(t, true)

	adapter := &Adapter{waitDelay: 50 * time.Millisecond, master: master}
	ctx, cancel := context.WithCancel(context.Background())
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

	err := adapter.Observe(context.Background(), func([]core.Listener) {})
	require.EqualValuesf(t, "transport_unavailable", core.ErrorDiagnostic(err), "Observe error = %v", err)
	commands := readCommands(t, logPath)
	lines := strings.Split(strings.TrimSpace(commands), "\n")
	require.Falsef(t, len(lines) != 1 || strings.Contains(lines[0], "-G"), "commands = %q, want one discovery command", lines)
}

func TestEnsureMasterValidatesAliasBeforeStarting(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "exit 1\n")

	_, err := adapter.ensureMaster(context.Background())
	require.EqualValuesf(t, "invalid_alias", core.ErrorDiagnostic(err), "ensureMaster error = %v", err)
	commands := readCommands(t, logPath)
	require.EqualValues(t, "-G dev", strings.TrimSpace(commands))
}

func TestEnsureMasterCleansLegacySocketBeforeStarting(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, `
case " $* " in
*" -O exit "*) exit 1 ;;
esac
`)
	adapter.configFile = filepath.Join(adapter.controlDirectory, "ssh-config")

	_, _ = adapter.ensureMaster(context.Background())
	commands := readCommands(t, logPath)
	lines := strings.Split(strings.TrimSpace(commands), "\n")
	legacyExit := strings.Join([]string{"-F", adapter.configFile, "-S master-%C -O exit dev"}, " ")
	legacyIndex := -1
	startIndex := -1
	for index, line := range lines {
		if line == legacyExit {
			legacyIndex = index
		}
		if strings.Contains(line, " -M -N -T -g -S "+adapter.controlPath()+" ") {
			startIndex = index
		}
	}
	require.Falsef(t, legacyIndex == -1 || startIndex == -1 || legacyIndex >= startIndex, "commands = %q; want legacy socket cleanup %q before replacement master", lines, legacyExit)
}

func TestForwardEndpointsAndRejectedInstallation(t *testing.T) {
	for _, tc := range []struct {
		direction              core.ForwardDirection
		local, remote          uint16
		flag, spec, diagnostic string
	}{
		{core.RemoteToLocal, 15173, 5173, "-L", "0.0.0.0:15173:127.0.0.1:5173", "local_port_conflict"},
		{core.LocalToRemote, 9222, 19222, "-R", "127.0.0.1:19222:127.0.0.1:9222", "remote_port_unavailable"},
	} {
		t.Run(string(tc.direction), func(t *testing.T) {
			target := core.ForwardTarget{Direction: tc.direction, LocalPort: tc.local, RemotePort: tc.remote}
			forward, err := controlForwardFor(target)
			require.NoError(t, err)
			require.Equal(t, controlForward{flag: tc.flag, spec: tc.spec}, forward)
			adapter, logPath := newLoggingAdapter(t, `
case " $* " in
*" -O forward "*) printf 'cannot listen to port\n' >&2; exit 1 ;;
*" -O check "*) exit 0 ;;
*" -O cancel "*) exit 1 ;;
esac
`)
			adapter.master = newTestMaster(t, false)
			err = adapter.Forward(context.Background(), target, func() { t.Fatal("rejected forward became ready") })
			require.Equal(t, tc.diagnostic, core.ErrorDiagnostic(err))
			select {
			case <-adapter.master.done:
				t.Fatal("rejected installation stopped shared master")
			default:
			}
			require.NotContains(t, readCommands(t, logPath), "-O cancel")
		})
	}
}

func TestRunControlUsesPrivateConfigurationAndExactFlag(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	forward := controlForward{flag: "-R", spec: "127.0.0.1:19222:127.0.0.1:9222"}
	require.NoError(t, adapter.runControl(context.Background(), "forward", &forward))
	commands := readCommands(t, logPath)
	want := strings.Join([]string{"-F /dev/null -S", adapter.controlPath(), "-O forward -o ExitOnForwardFailure=yes -R", forward.spec, "dev"}, " ")
	require.EqualValues(t, want, strings.TrimSpace(commands))
}

func TestStartMasterClearsConfiguredForwards(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.configFile = filepath.Join(adapter.controlDirectory, "ssh-config")
	master, err := adapter.startMaster()
	require.NoError(t, err)
	<-master.done
	commands := readCommands(t, logPath)
	want := strings.Join([]string{
		"-F", adapter.configFile, "-M -N -T -g -S", adapter.controlPath(),
		"-o ClearAllForwardings=yes -o ControlMaster=yes -o ControlPersist=no",
		"-o ServerAliveInterval=5 -o ServerAliveCountMax=3 -o ConnectTimeout=10 -o ConnectionAttempts=1 dev",
	}, " ")
	require.EqualValues(t, want, strings.TrimSpace(commands))
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
	require.Error(t, adapter.runControl(context.Background(), "check", nil), "hung control command succeeded")
	require.False(t, time.Since(start) > time.Second, "control command exceeded timeout")
}

func TestPerTargetConfigurationOverridesServiceConfiguration(t *testing.T) {
	adapter, _ := newLoggingAdapter(t, "")
	adapter.configFile = "/service/config"
	adapter.connectionArguments = []string{"-F", "/target/config", "-p", "2222"}
	got := strings.Join(adapter.configArguments(), " ")
	require.EqualValuesf(t, "-F /target/config -p 2222", got, "target settings lost: %s", got)
}

func TestRememberedHostControlsHaveIndependentIdentities(t *testing.T) {
	adapter, _ := newLoggingAdapter(t, "")
	oldPath := adapter.controlPath()
	adapter.controlIdentity = "dev"
	require.Equal(t, oldPath, adapter.controlPath(), "plain-host upgrade lost its existing control socket")
	adapter.controlIdentity = "dev-custom-port"
	require.False(t, adapter.controlPath() == oldPath, "different connection settings share a control socket")
}

func TestPublishedForwardReadinessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, probe, diagnostic string
		cancelFails             bool
	}{
		{"wildcard bind", "printf 'unsafe\\n'", "remote_bind_not_loopback", false},
		{"probe timeout", "exec sleep 3600", "remote_bind_unverified", false},
		{"probe failure", "printf 'awk unavailable\\n' >&2; exit 23", "remote_bind_unverified", false},
		{"cancellation failure", "printf 'unsafe\\n'", "remote_bind_not_loopback", true},
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
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ready := false
			err := adapter.Forward(ctx, core.ForwardTarget{Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222}, func() { ready = true })
			require.Equal(t, tc.diagnostic, core.ErrorDiagnostic(err))
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
