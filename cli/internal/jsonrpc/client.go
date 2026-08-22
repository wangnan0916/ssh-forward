package jsonrpc

import (
	"context"
	"fmt"
	"net"

	"github.com/creachadair/jrpc2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// DialConn checks the tiny protocol version and returns a remote Manager.
func DialConn(ctx context.Context, conn net.Conn) (core.Manager, error) {
	client := &managerClient{rpc: jrpc2.NewClient(newFrameChannel(conn, maxFrameBytes), nil)}
	var version versionResult
	if err := client.call(ctx, methodVersion, nil, &version); err != nil {
		_ = client.rpc.Close()
		return nil, fmt.Errorf("manager protocol version: %w", err)
	}
	if version.Version != protocolVersion {
		_ = client.rpc.Close()
		return nil, fmt.Errorf("manager speaks protocol %d, want %d", version.Version, protocolVersion)
	}
	return client, nil
}

type managerClient struct {
	rpc *jrpc2.Client
}

func (c *managerClient) call(ctx context.Context, method string, params, result any) error {
	return c.rpc.CallResult(ctx, method, params, result)
}

func (c *managerClient) Status(ctx context.Context) (core.Status, error) {
	var wrapped statusResult
	if err := c.call(ctx, methodStatus, nil, &wrapped); err != nil {
		return core.Status{}, err
	}
	return wrapped.Status, nil
}

func (c *managerClient) Close(context.Context) error {
	return c.rpc.Close()
}
