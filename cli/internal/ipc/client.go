package ipc

import (
	"context"
	"net"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	managerjsonrpc "github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
)

// Dial connects to the running singleton and negotiates the JSON-RPC v1
// session, returning a Manager whose operations are remote calls. The
// returned Manager is single-use: one process one session, matching the
// CLI's per-command lifetime.
func Dial(ctx context.Context, path string) (core.Manager, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return managerjsonrpc.Dial(ctx, conn)
}
