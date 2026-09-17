package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type managerClient struct {
	client    *http.Client
	transport *http.Transport
}

func dialManager(ctx context.Context, socket, version string) (*managerClient, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 2 * time.Second,
	}
	client := &managerClient{client: &http.Client{Transport: transport}, transport: transport}
	if _, err := client.readStatuses(ctx, version); err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return client, nil
}

func (c *managerClient) Reload(ctx context.Context, host string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://manager"+managerReloadPath+"?host="+url.QueryEscape(host), nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reload manager: %s", response.Status)
	}
	return nil
}

func (c *managerClient) AllStatuses(ctx context.Context) ([]core.Status, error) {
	return c.readStatuses(ctx, "")
}

func (c *managerClient) readStatuses(ctx context.Context, version string) ([]core.Status, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://manager"+managerStatusPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrIncompatibleManager
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manager status: %s", response.Status)
	}
	var result managerAllStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if result.ProtocolVersion != managerProtocolVersion || version != "" && result.ManagerVersion != version {
		return nil, ErrIncompatibleManager
	}
	return result.Hosts, nil
}

func (c *managerClient) Close(context.Context) error {
	c.transport.CloseIdleConnections()
	return nil
}

func waitManager(ctx context.Context, socket, version string, timeout time.Duration) (*managerClient, error) {
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
