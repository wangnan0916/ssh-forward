package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	managerStatusPath      = "/v1/status"
	managerProtocolVersion = 2
)

var ErrIncompatibleManager = errors.New("the running manager is incompatible")

func listenManager(path string) (net.Listener, error) {
	if socketLive(path) {
		return nil, errors.New("manager is already running")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return listener, nil
}

type managerStatus struct {
	ProtocolVersion int    `json:"protocol_version"`
	ManagerVersion  string `json:"manager_version"`
	core.Status
}

func managerHandler(manager core.Manager, version string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != managerStatusPath {
			http.NotFound(writer, request)
			return
		}
		status, err := manager.Status(request.Context())
		if err != nil {
			http.Error(writer, "manager unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(managerStatus{
			ProtocolVersion: managerProtocolVersion,
			ManagerVersion:  version,
			Status:          status,
		})
	})
}

type managerClient struct {
	client    *http.Client
	transport *http.Transport
}

func dialManager(ctx context.Context, socket, version string) (core.Manager, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 2 * time.Second,
	}
	client := &managerClient{client: &http.Client{Transport: transport}, transport: transport}
	if _, err := client.readStatus(ctx, version); err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return client, nil
}

func (c *managerClient) Status(ctx context.Context) (core.Status, error) {
	return c.readStatus(ctx, "")
}

func (c *managerClient) readStatus(ctx context.Context, version string) (core.Status, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://manager"+managerStatusPath, nil)
	if err != nil {
		return core.Status{}, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return core.Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return core.Status{}, ErrIncompatibleManager
	}
	if response.StatusCode != http.StatusOK {
		return core.Status{}, fmt.Errorf("manager status: %s", response.Status)
	}
	var status managerStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&status); err != nil {
		return core.Status{}, err
	}
	if status.ProtocolVersion != managerProtocolVersion || version != "" && status.ManagerVersion != version {
		return core.Status{}, ErrIncompatibleManager
	}
	return status.Status, nil
}

func (c *managerClient) Close(context.Context) error {
	c.transport.CloseIdleConnections()
	return nil
}

func waitManager(ctx context.Context, socket, version string, timeout time.Duration) (core.Manager, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		client, err := dialManager(ctx, socket, version)
		if err == nil {
			return client, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("manager did not become ready within %s: %w", timeout, lastErr)
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func socketLive(path string) bool {
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func waitSocketGone(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for socketLive(path) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}
