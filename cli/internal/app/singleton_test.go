package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

func TestConnectDialsLiveSocket(t *testing.T) {
	dir := filepath.Join(os.TempDir(), t.Name())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	layout := DefaultLayout()
	manager := core.NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = jsonrpc.Serve(ctx, layout.Socket, manager) }()
	if err := jsonrpc.Wait(context.Background(), layout.Socket, 3*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	_, err := Connect(context.Background(), Options{Layout: layout, HostFlag: "development"})
	if err == nil || !strings.Contains(err.Error(), "no Development Host configured") {
		t.Fatalf("Connect err = %v, want no-host singleton rejection", err)
	}
}

func TestServeLeavesLivePidAlone(t *testing.T) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("sf-pid-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	layout := DefaultLayout()
	manager := core.NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = jsonrpc.Serve(ctx, layout.Socket, manager) }()
	if err := jsonrpc.Wait(context.Background(), layout.Socket, 3*time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
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
