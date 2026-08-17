package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-forward/cli/internal/core"
)

// shortTempDir makes a real short Unix socket path: t.TempDir() nests under
// the system temp with a long prefix, exceeding the 104-byte sun_path
// bound on macOS. The product's real endpoint lives in a short runtime
// directory; these tests mirror that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("ipc-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// servingManager runs Serve on a temp-dir socket for the test duration.
func servingManager(t *testing.T) (string, core.Manager) {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "manager.sock")
	manager := core.NewManager()
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, path, manager) }()
	// Wait for the listener to accept connections.
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("manager socket never became ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not stop with the context")
		}
	})
	return path, manager
}

func TestDialHelloAndSnapshot(t *testing.T) {
	path, _ := servingManager(t)
	client, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 0 || snapshot.Host != nil {
		t.Fatalf("snapshot = %#v, want the fresh-manager shape (revision 0, no host)", snapshot)
	}
}

func TestDialExecuteMapsDomainErrors(t *testing.T) {
	path, _ := servingManager(t)
	client, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_, err = client.Execute(context.Background(), core.AddManualForward{
		CommandID: "op-1", Host: "development", RemotePort: 8080, Family: core.FamilyIPv4,
	})
	var domainError *core.DomainError
	if err == nil || !errors.As(err, &domainError) || domainError.Kind != core.ErrorUnknownHost {
		t.Fatalf("Execute err = %v, want ErrorUnknownHost", err)
	}
}

func TestDialWatchStreamsInitialSnapshot(t *testing.T) {
	path, _ := servingManager(t)
	client, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	stream, err := client.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	snapshot, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if snapshot.Revision != 0 {
		t.Fatalf("first snapshot revision = %d, want 0", snapshot.Revision)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("stream Close: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("client Close: %v", err)
	}
}

func TestServeRejectsALiveSingleton(t *testing.T) {
	path, _ := servingManager(t)
	manager := core.NewManager()
	defer func() { _ = manager.Close(context.Background()) }()
	if err := Serve(context.Background(), path, manager); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Serve err = %v, want ErrAlreadyRunning", err)
	}
}

func TestServeReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "manager.sock")
	// A socket file nobody listens on: the probe proves it stale.
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close() // leave the file behind, no live owner
	manager := core.NewManager()
	defer func() { _ = manager.Close(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, manager) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		client, err := Dial(context.Background(), path)
		if err == nil {
			_ = client.Close(context.Background())
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stale socket was not replaced: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not stop")
	}
}
