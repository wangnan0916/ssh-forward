//go:build integration

package sshhost_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestManagedForwardThroughDisposableDevelopmentHost(t *testing.T) {
	policiesPath := filepath.Join(t.TempDir(), "policies.jsonc")
	if err := os.WriteFile(policiesPath, []byte(fmt.Sprintf(`{
  "schema_version": 1,
  "policies": [
    {"id": "fixture-v4", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": %d, "to": %d}}]}
  ]
}`, fixturePortV4(), fixturePortV4())), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := app.NewManager(core.HostAlias(testHostAlias()), testHostConnector(t), app.NewFilePolicyReader(policiesPath).Source())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close Manager: %v", err)
		}
	})
	snapshot := waitForPolicyManagedForward(t, manager, fixturePortV4())
	waitForConnected(t, manager)
	localPort := managedLocalPort(t, snapshot, fixturePortV4())

	for _, host := range []string{"127.0.0.1", "::1"} {
		address := net.JoinHostPort(host, strconv.Itoa(int(localPort)))
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
}

func TestIPv6ManagedForwardThroughDisposableDevelopmentHost(t *testing.T) {
	policiesPath := filepath.Join(t.TempDir(), "policies.jsonc")
	if err := os.WriteFile(policiesPath, []byte(fmt.Sprintf(`{
  "schema_version": 1,
  "policies": [
    {"id": "fixture-v6", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": %d, "to": %d}}]}
  ]
}`, fixturePortV6(), fixturePortV6())), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := app.NewManager(core.HostAlias(testHostAlias()), testHostConnector(t), app.NewFilePolicyReader(policiesPath).Source())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close Manager: %v", err)
		}
	})
	snapshot := waitForPolicyManagedForward(t, manager, fixturePortV6())
	waitForConnected(t, manager)
	localPort := managedLocalPort(t, snapshot, fixturePortV6())

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(localPort)))
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("connect to IPv6 Managed Forward: %v", err)
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

func managedLocalPort(t *testing.T, snapshot core.Snapshot, remotePort uint16) uint16 {
	t.Helper()
	for _, forward := range snapshot.Host.Forwards {
		if forward.RemotePort == remotePort {
			return forward.AllocatedLocalPort
		}
	}
	t.Fatalf("no Managed Forward for port %d in %#v", remotePort, snapshot.Host.Forwards)
	return 0
}
