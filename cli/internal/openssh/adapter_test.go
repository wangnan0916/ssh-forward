//go:build darwin || linux

package openssh

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestNewRejectsSharedWritableControlDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{Executable: "/usr/bin/ssh", ControlDirectory: directory})
	if err == nil || !strings.Contains(err.Error(), "must not be writable by other users") {
		t.Fatalf("error = %v", err)
	}
}

func TestCloseHonorsCanceledContext(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	command := exec.Command("/bin/sh", "-c", `
trap '' TERM
: > "$1"
while :; do sleep 3600; done
`, "stubborn-master", readyPath)
	configureProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	master := &sshMaster{command: command, done: make(chan struct{})}
	go master.wait()
	t.Cleanup(func() {
		select {
		case <-master.done:
		default:
			_ = killProcess(master.command)
			<-master.done
		}
	})
	waitForFile(t, readyPath)

	adapter := &Adapter{
		waitDelay: 50 * time.Millisecond,
		masters:   map[core.HostAlias]*sshMaster{"dev": master},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapter.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v, want context.Canceled", err)
	}
	select {
	case <-master.done:
	case <-time.After(2 * time.Second):
		t.Fatal("master survived background cleanup")
	}
}

func TestObserveReusesMasterWithoutAliasValidation(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.masters["dev"] = &sshMaster{done: make(chan struct{})}

	err := adapter.Observe(context.Background(), "dev", func([]core.Listener) {})
	if core.ErrorDiagnostic(err) != "transport_unavailable" {
		t.Fatalf("Observe error = %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	if len(lines) != 1 || strings.Contains(lines[0], "-G") {
		t.Fatalf("commands = %q, want one discovery command", lines)
	}
}

func TestEnsureMasterValidatesAliasBeforeStarting(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "exit 1\n")

	_, err := adapter.ensureMaster(context.Background(), "dev")
	if core.ErrorDiagnostic(err) != "invalid_alias" {
		t.Fatalf("ensureMaster error = %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if command := strings.TrimSpace(string(commands)); command != "-G dev" {
		t.Fatalf("command = %q, want %q", command, "-G dev")
	}
}

func TestEnsureMasterCleansLegacySocketBeforeStarting(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, `
case " $* " in
*" -O exit "*) exit 1 ;;
esac
`)
	adapter.configFile = filepath.Join(adapter.controlDirectory, "ssh-config")

	_, _ = adapter.ensureMaster(context.Background(), "dev")
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	legacyExit := strings.Join([]string{
		"-F", adapter.configFile, "-S master-%C -O exit dev",
	}, " ")
	legacyIndex := -1
	startIndex := -1
	for index, line := range lines {
		if line == legacyExit {
			legacyIndex = index
		}
		if strings.Contains(line, " -M -N -T -S "+adapter.controlPath("dev")+" ") {
			startIndex = index
		}
	}
	if legacyIndex == -1 || startIndex == -1 || legacyIndex >= startIndex {
		t.Fatalf(
			"commands = %q; want legacy socket cleanup %q before replacement master",
			lines, legacyExit,
		)
	}
}

func TestControlForwardForUsesDirectionSpecificLoopbackEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		target core.ForwardTarget
		want   controlForward
	}{
		{
			name: "remote to local",
			target: core.ForwardTarget{
				Direction: core.RemoteToLocal, RemotePort: 5173, LocalPort: 15173,
			},
			want: controlForward{flag: "-L", spec: "127.0.0.1:15173:127.0.0.1:5173"},
		},
		{
			name: "local to remote",
			target: core.ForwardTarget{
				Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222,
			},
			want: controlForward{flag: "-R", spec: "127.0.0.1:19222:127.0.0.1:9222"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := controlForwardFor(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("control forward = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPublishedForwardRejectsRemoteWildcardBind(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, `
last_argument=
for argument do last_argument=$argument; done
if [ "$last_argument" = 19222 ]; then
    cat >/dev/null
    printf 'unsafe\n'
fi
`)
	adapter.masters["dev"] = &sshMaster{done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := false
	err := adapter.Forward(ctx, "dev", core.ForwardTarget{
		Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222,
	}, func() { ready = true })
	if diagnostic := core.ErrorDiagnostic(err); diagnostic != "remote_bind_not_loopback" {
		commands, _ := os.ReadFile(logPath)
		t.Fatalf(
			"Forward error = %v, diagnostic = %q, ready = %t, commands = %q; want remote_bind_not_loopback",
			err, diagnostic, ready, commands,
		)
	}
	if ready {
		t.Fatal("Published Forward became ready before rejecting its wildcard bind")
	}
}

func TestPublishedForwardBoundsRemoteBindProbe(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, `
last_argument=
for argument do last_argument=$argument; done
if [ "$last_argument" = 19222 ]; then
    exec sleep 3600
fi
`)
	adapter.readyTimeout = 50 * time.Millisecond
	adapter.masters["dev"] = &sshMaster{done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ready := false
	err := adapter.Forward(ctx, "dev", core.ForwardTarget{
		Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222,
	}, func() { ready = true })
	if diagnostic := core.ErrorDiagnostic(err); diagnostic != "remote_bind_unverified" {
		t.Fatalf("Forward error = %v, diagnostic = %q; want remote_bind_unverified", err, diagnostic)
	}
	if ctx.Err() != nil {
		t.Fatalf("outer context expired before the readiness timeout: %v", ctx.Err())
	}
	if ready {
		t.Fatal("Published Forward became ready before its remote bind was verified")
	}
	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(commands), "-O cancel") {
		t.Fatalf("commands = %q; want remote forward cancellation", commands)
	}
}

func TestPublishedForwardStopsMasterWhenCancellationFails(t *testing.T) {
	adapter, _ := newLoggingAdapter(t, `
case " $* " in
*" -O cancel "*) exit 1 ;;
esac
last_argument=
for argument do last_argument=$argument; done
if [ "$last_argument" = 19222 ]; then
    printf 'unsafe\n'
fi
`)
	adapter.waitDelay = 50 * time.Millisecond
	master := newTerminableTestMaster(t)
	adapter.masters["dev"] = master

	err := adapter.Forward(context.Background(), "dev", core.ForwardTarget{
		Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222,
	}, func() { t.Fatal("Published Forward became ready with a wildcard bind") })
	if diagnostic := core.ErrorDiagnostic(err); diagnostic != "remote_bind_not_loopback" {
		t.Fatalf("Forward diagnostic = %q; want remote_bind_not_loopback", diagnostic)
	}
	select {
	case <-master.done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("master remained active after remote forward cancellation failed")
	}
}

func TestRejectedForwardDoesNotStopMaster(t *testing.T) {
	tests := []struct {
		name       string
		target     core.ForwardTarget
		diagnostic string
	}{
		{
			name: "local to remote",
			target: core.ForwardTarget{
				Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222,
			},
			diagnostic: "remote_port_unavailable",
		},
		{
			name: "remote to local",
			target: core.ForwardTarget{
				Direction: core.RemoteToLocal, LocalPort: 15173, RemotePort: 5173,
			},
			diagnostic: "local_port_conflict",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, logPath := newLoggingAdapter(t, `
case " $* " in
*" -O forward "*) printf 'cannot listen to port\n' >&2; exit 1 ;;
*" -O check "*) exit 0 ;;
*" -O cancel "*) exit 1 ;;
esac
`)
			adapter.waitDelay = 50 * time.Millisecond
			master := newTerminableTestMaster(t)
			adapter.masters["dev"] = master

			err := adapter.Forward(context.Background(), "dev", test.target, func() {
				t.Fatal("rejected Forward became ready")
			})
			if diagnostic := core.ErrorDiagnostic(err); diagnostic != test.diagnostic {
				t.Fatalf("Forward diagnostic = %q; want %s", diagnostic, test.diagnostic)
			}
			commands, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			select {
			case <-master.done:
				t.Fatalf("rejected forward stopped the shared master; commands = %q", commands)
			default:
			}
			if strings.Contains(string(commands), "-O cancel") {
				t.Fatalf("rejected forward attempted cancellation; commands = %q", commands)
			}
		})
	}
}

func TestPublishedForwardReportsFailedRemoteBindProbe(t *testing.T) {
	adapter, _ := newLoggingAdapter(t, `
last_argument=
for argument do last_argument=$argument; done
if [ "$last_argument" = 19222 ]; then
    printf 'awk unavailable\n' >&2
    exit 23
fi
`)
	adapter.masters["dev"] = &sshMaster{done: make(chan struct{})}
	err := adapter.Forward(context.Background(), "dev", core.ForwardTarget{
		Direction: core.LocalToRemote, LocalPort: 9222, RemotePort: 19222,
	}, func() { t.Fatal("Published Forward became ready without verifying its remote bind") })
	if diagnostic := core.ErrorDiagnostic(err); diagnostic != "remote_bind_unverified" {
		t.Fatalf("Forward diagnostic = %q; want remote_bind_unverified", diagnostic)
	}
}

func TestRunControlUsesPrivateConfigurationAndExactFlag(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	forward := controlForward{flag: "-R", spec: "127.0.0.1:19222:127.0.0.1:9222"}
	if err := adapter.runControl(context.Background(), "dev", "forward", &forward); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"-F /dev/null -S", adapter.controlPath("dev"),
		"-O forward -o ExitOnForwardFailure=yes -R",
		forward.spec, "dev",
	}, " ")
	if got := strings.TrimSpace(string(commands)); got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestStartMasterClearsConfiguredForwards(t *testing.T) {
	adapter, logPath := newLoggingAdapter(t, "")
	adapter.configFile = filepath.Join(adapter.controlDirectory, "ssh-config")
	master, err := adapter.startMaster("dev")
	if err != nil {
		t.Fatal(err)
	}
	<-master.done
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"-F", adapter.configFile, "-M -N -T -S", adapter.controlPath("dev"),
		"-o ClearAllForwardings=yes -o ControlMaster=yes -o ControlPersist=no dev",
	}, " ")
	if got := strings.TrimSpace(string(commands)); got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func newLoggingAdapter(t *testing.T, scriptSuffix string) (*Adapter, string) {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands")
	executable := filepath.Join(directory, "ssh")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_FORWARD_TEST_LOG"
` + scriptSuffix
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Adapter{
		executable:       executable,
		controlDirectory: directory,
		readyTimeout:     time.Second,
		waitDelay:        time.Second,
		environment:      append(approvedEnvironment(), "SSH_FORWARD_TEST_LOG="+logPath),
		masters:          make(map[core.HostAlias]*sshMaster),
	}, logPath
}

func newTerminableTestMaster(t *testing.T) *sshMaster {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", `
trap 'exit 0' TERM
while :; do sleep 3600; done
`)
	configureProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	master := &sshMaster{
		command: command,
		stderr:  &boundedBuffer{limit: maxStderrTailBytes},
		done:    make(chan struct{}),
	}
	go master.wait()
	t.Cleanup(func() {
		select {
		case <-master.done:
		default:
			_ = killProcess(command)
			<-master.done
		}
	})
	return master
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
