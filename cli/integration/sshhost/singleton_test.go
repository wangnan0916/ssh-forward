//go:build integration

package sshhost_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/ipc"
	"ssh-forward/cli/internal/openssh"
)

// TestSingletonManagerThroughDisposableDevelopmentHost pins ADR-0016 end
// to end over a real Development Host: the singleton (ipc.Serve) owns one
// Manager, and every operation below is a socket client call — manual
// forward allocation, snapshot visibility, connection state, removal, a
// second serve refusal, and a watch stream.
func TestSingletonManagerThroughDisposableDevelopmentHost(t *testing.T) {
	adapter, err := openssh.New(openssh.Options{
		Executable:   "/usr/bin/ssh",
		ConfigFile:   isolatedSSHConfig(t),
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("create OpenSSH Adapter: %v", err)
	}
	manager := app.NewManager(core.HostAlias(testHostAlias()), adapter)
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
	go func() { served <- ipc.Serve(serveCtx, endpoint, manager) }()
	waitForEndpointReady(t, endpoint)

	client, err := ipc.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Dial the singleton: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })

	// 1. A Manual Forward through the singleton: the allocation happens in
	// the serve process and stays there.
	outcome, err := client.Execute(context.Background(), core.AddManualForward{
		CommandID:  "e2e-manual",
		Host:       core.HostAlias(testHostAlias()),
		RemotePort: fixturePortV4(),
		Family:     core.FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward through the singleton: %v", err)
	}
	if outcome.Kind != core.OutcomeForwardAdded || outcome.Forward.AllocatedLocalPort == 0 ||
		outcome.Forward.RemotePort != fixturePortV4() {
		t.Fatalf("add outcome = %+v, want forward_added on port %d", outcome, fixturePortV4())
	}

	// 2. The client's snapshot follows the singleton's state: connected,
	// with the forward visible.
	waitForSnapshot(t, client, "singleton connects and shows the forward", func(snapshot core.Snapshot) bool {
		if snapshot.Host == nil || snapshot.Host.Connection != core.ConnectionConnected {
			return false
		}
		for _, forward := range snapshot.Host.Forwards {
			if forward.ID == outcome.Forward.ID && forward.AllocatedLocalPort == outcome.Forward.AllocatedLocalPort {
				return true
			}
		}
		return false
	})

	// 3. Watch through the singleton streams the same state.
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

	// 4. Removal through the singleton finds and tears down the forward.
	removed, err := client.Execute(context.Background(), core.RemoveForward{
		CommandID: "e2e-remove",
		ForwardID: outcome.Forward.ID,
	})
	if err != nil {
		t.Fatalf("remove through the singleton: %v", err)
	}
	if removed.Kind != core.OutcomeForwardRemoved || removed.Forward.ID != outcome.Forward.ID {
		t.Fatalf("remove outcome = %+v, want forward_removed for %s", removed, outcome.Forward.ID)
	}

	// 5. The singleton is one: a second serve is refused while it runs.
	second := app.NewManager(core.HostAlias(testHostAlias()), adapter)
	defer func() { _ = second.Close(context.Background()) }()
	if err := ipc.Serve(context.Background(), endpoint, second); !errors.Is(err, ipc.ErrAlreadyRunning) {
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

func waitForEndpointReady(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("singleton endpoint never became ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
