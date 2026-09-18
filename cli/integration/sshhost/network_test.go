//go:build integration

package sshhost

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func firstNonLoopbackIPv4(t *testing.T) (string, bool) {
	t.Helper()
	interfaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				return ip.String(), true
			}
		}
	}
	return "", false
}

func availableLocalPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	require.NoError(t, listener.Close())
	return port
}

func occupiedLocalPort(t *testing.T) (uint16, net.Listener) {
	t.Helper()
	for range 100 {
		blocker, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		port := blocker.Addr().(*net.TCPAddr).Port
		if port < 65535 {
			return uint16(port), blocker
		}
		_ = blocker.Close()
	}
	t.Fatal("could not reserve an occupied port below 65535")
	return 0, nil
}

func startLocalEchoServer(t *testing.T) uint16 {
	port, _ := startLocalEchoServerOnPort(t, 0)
	return port
}

func startLocalEchoServerOnPort(t *testing.T, port uint16) (uint16, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	require.NoError(t, err)
	stop := func() { _ = listener.Close() }
	t.Cleanup(stop)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port), stop
}

func localPortOpen(port uint16) bool {
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 50*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
