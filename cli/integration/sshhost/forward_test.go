//go:build integration

package sshhost

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

func TestDiscoversAndForwardsRemoteLoopbackPort(t *testing.T) {
	config := os.Getenv("SSH_FORWARD_TEST_SSH_CONFIG")
	if config == "" {
		t.Fatal("SSH_FORWARD_TEST_SSH_CONFIG is not set; run scripts/test-integration")
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: ssh, ConfigFile: config})
	if err != nil {
		t.Fatal(err)
	}
	host := os.Getenv("SSH_FORWARD_TEST_HOST_ALIAS")
	if host == "" {
		host = "ssh-forward-test-host"
	}
	port := uint16(38080)
	if value := os.Getenv("SSH_FORWARD_FIXTURE_PORT_V4"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		port = uint16(parsed)
	}
	manager := core.NewManager(core.HostAlias(host), adapter, []uint16{port})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Forwards) == 1 && status.Forwards[0].State == core.ForwardActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		t.Fatalf("dial forwarded port: %v", err)
	}
	tcp := connection.(*net.TCPConn)
	defer tcp.Close()
	if _, err := tcp.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(tcp)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "hello" {
		t.Fatalf("reply = %q", reply)
	}
}
