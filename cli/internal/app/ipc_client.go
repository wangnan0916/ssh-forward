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
	client *http.Client
}

func dialManager(ctx context.Context, socket, version string) (*managerClient, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 2 * time.Second,
	}
	client := &managerClient{client: &http.Client{Transport: transport}}
	if _, err := client.readStatuses(ctx, version); err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return client, nil
}

func (c *managerClient) Reload(ctx context.Context, host string) error {
	return c.request(ctx, http.MethodPost, managerReloadPath+"?host="+url.QueryEscape(host), http.StatusNoContent, nil)
}

func (c *managerClient) AllStatuses(ctx context.Context) ([]core.Status, error) {
	return c.readStatuses(ctx, "")
}

func (c *managerClient) readStatuses(ctx context.Context, version string) ([]core.Status, error) {
	var result managerAllStatus
	if err := c.request(ctx, http.MethodGet, managerStatusPath, http.StatusOK, &result); err != nil {
		return nil, err
	}
	if result.ProtocolVersion != managerProtocolVersion || version != "" && result.ManagerVersion != version {
		return nil, ErrIncompatibleManager
	}
	return result.Hosts, nil
}

func (c *managerClient) request(ctx context.Context, method, path string, expected int, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://manager"+path, nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if path == managerStatusPath && response.StatusCode == http.StatusNotFound {
		return ErrIncompatibleManager
	}
	if response.StatusCode != expected {
		return fmt.Errorf("manager %s: %s", path, response.Status)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(result)
	}
	return nil
}

func (c *managerClient) Close(context.Context) error {
	c.client.CloseIdleConnections()
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
