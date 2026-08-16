//go:build integration

package sshhost_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

func TestManualForwardThroughDisposableDevelopmentHost(t *testing.T) {
	config := isolatedSSHConfig(t)
	adapter, err := openssh.New(openssh.Options{
		Executable:   "/usr/bin/ssh",
		ConfigFile:   config,
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
	added, err := manager.Execute(context.Background(), core.AddManualForward{
		CommandID:  core.CommandID("integration-add"),
		Host:       core.HostAlias(testHostAlias()),
		RemotePort: fixturePortV4(),
		Family:     core.FamilyAuto,
	})
	if err != nil {
		t.Fatalf("add Manual Forward: %v", err)
	}
	waitForConnected(t, manager)

	for _, host := range []string{"127.0.0.1", "::1"} {
		address := net.JoinHostPort(host, strconv.Itoa(int(added.Forward.AllocatedLocalPort)))
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatalf("connect to Local Endpoint %s: %v", address, err)
		}
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			_ = connection.Close()
			t.Fatalf("set Local Endpoint deadline: %v", err)
		}
		request := fmt.Sprintf("request-from-%s", host)
		if _, err := connection.Write([]byte(request)); err != nil {
			_ = connection.Close()
			t.Fatalf("write through Local Endpoint %s: %v", address, err)
		}
		if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
			_ = connection.Close()
			t.Fatalf("half-close Local Endpoint %s: %v", address, err)
		}
		response, err := io.ReadAll(connection)
		_ = connection.Close()
		if err != nil {
			t.Fatalf("read through Local Endpoint %s: %v", address, err)
		}
		if got, want := string(response), "fixture:"+request; got != want {
			t.Fatalf("response through %s = %q, want %q", address, got, want)
		}
	}

	if _, err := manager.Execute(context.Background(), core.RemoveForward{
		CommandID: core.CommandID("integration-remove"),
		ForwardID: added.Forward.ID,
	}); err != nil {
		t.Fatalf("remove Manual Forward: %v", err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(added.Forward.AllocatedLocalPort)))
	connection, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("removed Local Endpoint still accepts connections")
	}
}

func TestIPv6ManualForwardThroughDisposableDevelopmentHost(t *testing.T) {
	config := isolatedSSHConfig(t)
	adapter, err := openssh.New(openssh.Options{
		Executable:   "/usr/bin/ssh",
		ConfigFile:   config,
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
	added, err := manager.Execute(context.Background(), core.AddManualForward{
		CommandID:  core.CommandID("integration-ipv6"),
		Host:       core.HostAlias(testHostAlias()),
		RemotePort: fixturePortV6(),
		Family:     core.FamilyIPv6,
	})
	if err != nil {
		t.Fatalf("add IPv6 Manual Forward: %v", err)
	}
	waitForConnected(t, manager)

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(added.Forward.AllocatedLocalPort)))
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("connect to IPv6 Manual Forward: %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set Local Endpoint deadline: %v", err)
	}
	if _, err := connection.Write([]byte("ipv6-request")); err != nil {
		t.Fatalf("write IPv6 request: %v", err)
	}
	if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-close IPv6 request: %v", err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read IPv6 response: %v", err)
	}
	if got, want := string(response), "fixture:ipv6-request"; got != want {
		t.Fatalf("IPv6 response = %q, want %q", got, want)
	}
}

func waitForConnected(t *testing.T, manager core.Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Manager Snapshot: %v", err)
		}
		if snapshot.Host != nil && snapshot.Host.Connection == core.ConnectionConnected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Development Host did not connect; last Snapshot: %#v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
