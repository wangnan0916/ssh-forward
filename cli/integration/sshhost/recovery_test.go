//go:build integration

package sshhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

// Drop traffic without closing the TCP connection, as happens when a server
// or network disappears without delivering FIN/RST. Killing the local master
// alone cannot test detection of this failure mode.
func TestForwardsRecoverAfterSilentConnectionLoss(t *testing.T) {
	environment := loadTestEnvironment(t)
	output, err := exec.Command(environment.ssh, "-F", environment.config, "-G", environment.host).Output()
	if err != nil {
		t.Fatal(err)
	}
	hostname, port := "", ""
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "hostname":
			hostname = fields[1]
		case "port":
			port = fields[1]
		}
	}
	if hostname == "" || port == "" {
		t.Fatal("missing fixture SSH endpoint")
	}
	proxy, loseConnection := blackholeProxy(t, net.JoinHostPort(hostname, port))
	baseConfig, err := os.ReadFile(environment.config)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "ssh-config")
	// Explicitly disable user-config keepalives to prove the product's command
	// options enforce recovery even when the user's SSH config does not.
	prefix := fmt.Sprintf("Host %s\n Hostname 127.0.0.1\n Port %d\n HostKeyAlias [%s]:%s\n ServerAliveInterval 0\n ServerAliveCountMax 999\n", environment.host, proxy.Addr().(*net.TCPAddr).Port, hostname, port)
	if err := os.WriteFile(config, append([]byte(prefix), baseConfig...), 0600); err != nil {
		t.Fatal(err)
	}
	adapter, err := openssh.New(openssh.Options{Executable: environment.ssh, ConfigFile: config, ControlDirectory: environment.controlDirectory})
	if err != nil {
		t.Fatal(err)
	}
	remote := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_V4", 38080)
	local := availableLocalPort(t)
	published := fixturePort(t, "SSH_FORWARD_FIXTURE_PORT_REVERSE", 38085)
	service := startLocalEchoServer(t)
	manager := core.NewManager(core.HostAlias(environment.host), adapter, core.ForwardingIntent{
		RememberedForwards: []core.RememberedForward{{RemotePort: remote, LocalPort: local}},
		PublishedForwards:  []core.PublishedForward{{LocalPort: service, RemotePort: published}},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	active := func(status core.Status) bool {
		return status.Discovery.State == core.DiscoveryActive && len(status.Forwards) == 2 && allForwardsActive(status.Forwards)
	}
	waitForStatus(t, manager, active)
	wantForwardedEcho(t, local, "before-silent-loss")
	wantPublishedEcho(t, environment, published, "before-silent-loss")
	loseConnection()
	// The proxy blackholes the old connection forever; only a newly established
	// connection can restore discovery and both directions of forwarding.
	waitForStatus(t, manager, func(status core.Status) bool { return !active(status) }, 30*time.Second)
	waitForStatus(t, manager, active)
	wantForwardedEcho(t, local, "after-silent-loss")
	wantPublishedEcho(t, environment, published, "after-silent-loss")
}

func blackholeProxy(t *testing.T, target string) (net.Listener, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var generation atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer client.Close()
				stopClient := context.AfterFunc(ctx, func() { _ = client.Close() })
				defer stopClient()
				server, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", target)
				if err != nil {
					return
				}
				defer server.Close()
				stopServer := context.AfterFunc(ctx, func() { _ = server.Close() })
				defer stopServer()
				epoch := generation.Load()
				copyTraffic := func(dst, src net.Conn) {
					buffer := make([]byte, 32768)
					for {
						n, err := src.Read(buffer)
						if n > 0 && generation.Load() == epoch {
							if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
								return
							}
						}
						if err != nil {
							return
						}
					}
				}
				copied := make(chan struct{})
				go func() { copyTraffic(server, client); _ = server.Close(); close(copied) }()
				copyTraffic(client, server)
				_ = client.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		cancel()
		workers.Wait()
	})
	t.Log("silent-loss proxy port " + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	return listener, func() { generation.Add(1) }
}
