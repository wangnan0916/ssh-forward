//go:build integration

package sshhost_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

// TestSingletonManagerThroughDisposableDevelopmentHost pins ADR-0016 end
// to end over a real Development Host: the singleton (jsonrpc.Serve) owns one
// Manager, and every operation below is a socket client call — snapshot
// visibility, connection state, a second serve refusal, and a watch stream.
func TestSingletonManagerThroughDisposableDevelopmentHost(t *testing.T) {
	adapter := testHostConnector(t)
	manager := testConfiguredManager(t, adapter)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close Manager: %v", err)
		}
	})

	endpoint := filepath.Join(os.TempDir(), fmt.Sprintf("sf-singleton-%d.sock", time.Now().UnixNano()))
	serveCtx, serveCancel := context.WithCancel(context.Background())
	t.Cleanup(serveCancel)
	served := make(chan error, 1)
	go func() { served <- jsonrpc.Serve(serveCtx, endpoint, manager) }()
	if err := jsonrpc.Wait(context.Background(), endpoint, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	client, err := jsonrpc.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Dial the singleton: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	waitForSnapshot(t, client, "singleton connects", func(snapshot core.Snapshot) bool {
		return snapshot.Host != nil && snapshot.Host.Connection == core.ConnectionConnected
	})

	stream, err := client.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch through the singleton: %v", err)
	}
	defer stream.Close()
	initial, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Watch initial snapshot: %v", err)
	}
	if initial.Revision == 0 || initial.Host == nil || initial.Host.Connection != core.ConnectionConnected {
		t.Fatalf("watch initial = %#v, want the connected singleton state", initial)
	}

	second := testConfiguredManager(t, adapter)
	defer func() { _ = second.Close(context.Background()) }()
	if err := jsonrpc.Serve(context.Background(), endpoint, second); !errors.Is(err, jsonrpc.ErrAlreadyRunning) {
		t.Fatalf("second serve err = %v, want ErrAlreadyRunning", err)
	}

	serveCancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not stop with its context")
	}
}
