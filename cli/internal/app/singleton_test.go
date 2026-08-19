package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

func isolatedLayout(t *testing.T) Layout {
	t.Helper()
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("sf-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	return DefaultLayout()
}

func startServing(t *testing.T, layout Layout, manager core.Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = jsonrpc.Serve(ctx, layout.Socket, manager) }()
	if err := jsonrpc.Wait(context.Background(), layout.Socket, 3*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestConnectDialsLiveSocket(t *testing.T) {
	layout := isolatedLayout(t)
	manager := core.NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	startServing(t, layout, manager)
	_, err := Connect(context.Background(), Options{Layout: layout, HostFlag: "development"})
	if err == nil || !strings.Contains(err.Error(), "no Development Host configured") {
		t.Fatalf("Connect err = %v, want no-host singleton rejection", err)
	}
}

func TestConnectReportsLiveSnapshotError(t *testing.T) {
	layout := isolatedLayout(t)
	startServing(t, layout, snapshotErrorManager{})
	_, err := Connect(context.Background(), Options{Layout: layout, HostFlag: "development"})
	if err == nil || !strings.Contains(err.Error(), "could not read the running manager") {
		t.Fatalf("Connect err = %v, want the Snapshot RPC error", err)
	}
	if strings.Contains(err.Error(), "no Development Host configured") {
		t.Fatalf("Connect collapsed the RPC error into no-host: %v", err)
	}
}

func TestConnectReportsIncompatibleLiveManager(t *testing.T) {
	layout := isolatedLayout(t)
	listener, err := net.Listen("unix", layout.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	_, err = Connect(context.Background(), Options{Layout: layout, HostFlag: "development"})
	if err == nil || !errors.Is(err, ErrIncompatibleManager) {
		t.Fatalf("Connect err = %v, want ErrIncompatibleManager", err)
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	if err := Stop(isolatedLayout(t)); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stop = %v, want ErrNotRunning", err)
	}
}

func TestStopDoesNotKillPidWithoutLiveSocket(t *testing.T) {
	layout := isolatedLayout(t)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if err := os.WriteFile(layout.PID, []byte(fmt.Sprintf("%d\n", child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Stop(layout); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stop = %v, want ErrNotRunning", err)
	}
	if !PIDAlive(child.Process.Pid) {
		t.Fatalf("Stop killed a pid that did not own the manager socket")
	}
}

type snapshotErrorManager struct{}

func (snapshotErrorManager) Snapshot(context.Context) (core.Snapshot, error) {
	return core.Snapshot{}, errors.New("boom")
}

func (snapshotErrorManager) Watch(context.Context) (core.SnapshotStream, error) {
	return nil, errors.New("unused")
}

func (snapshotErrorManager) Close(context.Context) error {
	return nil
}

func TestTakeServeEnvCopiesOptions(t *testing.T) {
	for _, tc := range []struct {
		name, serve, host, policies, sshConfig string
		take                                   func(*Options) bool
	}{
		{"manager", envManagerServe, envManagerHost, envManagerPolicies, envManagerSSHConfig, TakeManagerServeEnv},
		{"ui", envUIServe, envUIHost, envUIPolicies, envUISSHConfig, TakeUIServeEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.serve, "1")
			t.Setenv(tc.host, "devbox")
			t.Setenv(tc.policies, "/tmp/policies.jsonc")
			t.Setenv(tc.sshConfig, "/tmp/ssh-config")
			opts := Options{}
			if !tc.take(&opts) {
				t.Fatal("take = false, want the autospawn encoding")
			}
			if opts.HostFlag != "devbox" || opts.PoliciesPath != "/tmp/policies.jsonc" || opts.SSHConfigPath != "/tmp/ssh-config" {
				t.Fatalf("opts = %+v, want host/policies/ssh-config from env", opts)
			}
			if os.Getenv(tc.serve) != "" {
				t.Fatal("serve env should be consumed so a later Run can parse argv")
			}
			if tc.take(&Options{}) {
				t.Fatal("second take = true, want consumed")
			}
		})
	}
}

func TestServeLeavesLivePidAlone(t *testing.T) {
	layout := isolatedLayout(t)
	manager := core.NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	startServing(t, layout, manager)
	if err := os.WriteFile(layout.PID, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Serve(context.Background(), Options{Layout: layout, HostFlag: "development"})
	if !errors.Is(err, jsonrpc.ErrAlreadyRunning) {
		t.Fatalf("Serve err = %v, want ErrAlreadyRunning", err)
	}
	raw, err := os.ReadFile(layout.PID)
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	if string(raw) != "4242\n" {
		t.Fatalf("pid file = %q, want the live singleton's pid left alone", raw)
	}
}
