package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

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

func (c *managerClient) UpdateIntent(ctx context.Context, intent core.ForwardingIntent) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(intent); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://manager"+managerIntentPath, &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrIncompatibleManager
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("manager intent update: %s", response.Status)
	}
	return nil
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
