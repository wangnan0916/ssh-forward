//go:build integration

package sshhost

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func wantPublishedEcho(t *testing.T, environment testEnvironment, remotePort uint16, message string) {
	t.Helper()
	script := fmt.Sprintf("printf '%%s' '%s' | /usr/bin/socat - TCP4:127.0.0.1:%d\n", message, remotePort)
	output, err := runRemoteScript(environment, script)
	require.Falsef(t, err != nil || output != message, "remote published echo = %q, %v; want %q", output, err, message)
}

func wantRemoteLoopbackListener(t *testing.T, environment testEnvironment, port uint16) {
	t.Helper()
	output, err := runRemoteScript(environment, fmt.Sprintf("/usr/bin/ss -H -ltn 'sport = :%d'\n", port))
	require.Falsef(t, err != nil || !strings.Contains(output, "127.0.0.1:"+strconv.Itoa(int(port))), "remote listener is not loopback-only: %q, %v", output, err)
}

func wantRemotePortClosed(t *testing.T, environment testEnvironment, port uint16) {
	t.Helper()
	output, err := runRemoteScript(environment, fmt.Sprintf("/usr/bin/ss -H -ltn 'sport = :%d'\n", port))
	require.Falsef(t, err != nil || strings.TrimSpace(output) != "", "remote port %d remains open: %q, %v", port, output, err)
}

func allForwardsActive(forwards []core.ForwardStatus) bool {
	if len(forwards) == 0 {
		return false
	}
	for _, forward := range forwards {
		if forward.State != core.ForwardActive {
			return false
		}
	}
	return true
}

func wantForwardedEcho(t *testing.T, port uint16, message string) {
	t.Helper()
	wantForwardedEchoAt(t, "127.0.0.1", port, message)
}

func wantForwardedEchoAt(t *testing.T, address string, port uint16, message string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", net.JoinHostPort(address, strconv.Itoa(int(port))), time.Second)
	require.NoErrorf(t, err, "dial forwarded port at %s: %v", address, err)
	tcp := connection.(*net.TCPConn)
	defer tcp.Close()
	if _, err := tcp.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, tcp.CloseWrite())
	reply, err := io.ReadAll(tcp)
	require.NoError(t, err)
	require.EqualValuesf(t, message, string(reply), "reply = %q, want %q", reply, message)
}

func waitForStatus(t *testing.T, manager core.Manager, condition func(core.Status) bool, timeout ...time.Duration) core.Status {
	t.Helper()
	limit := 15 * time.Second
	if len(timeout) > 0 {
		limit = timeout[0]
	}
	deadline := time.Now().Add(limit)
	var status core.Status
	for time.Now().Before(deadline) {
		var err error
		status, err = manager.Status(context.Background())
		require.NoError(t, err)
		if condition(status) {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("manager status did not converge: %#v", status)
	return core.Status{}
}

func isSocat(listener core.Listener) bool {
	return strings.HasPrefix(listener.App, "socat")
}

func listenersByPort(listeners []core.Listener) map[uint16]core.Listener {
	indexed := make(map[uint16]core.Listener, len(listeners))
	for _, listener := range listeners {
		indexed[listener.Port] = listener
	}
	return indexed
}
