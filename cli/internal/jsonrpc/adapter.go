package jsonrpc

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	"github.com/creachadair/jrpc2/handler"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// ServeConn runs one JSON-RPC session on conn until it ends.
func ServeConn(ctx context.Context, conn net.Conn, manager core.Manager) error {
	frames := newFrameChannel(conn, maxFrameBytes)
	methods := handler.Map{
		methodVersion: func(context.Context, *jrpc2.Request) (any, error) {
			return versionResult{Version: protocolVersion}, nil
		},
		methodStatus: func(ctx context.Context, _ *jrpc2.Request) (any, error) {
			status, err := manager.Status(ctx)
			if err != nil {
				return nil, internalError()
			}
			return statusResult{Status: status}, nil
		},
	}
	stopSession := context.AfterFunc(ctx, func() { _ = frames.Close() })
	defer stopSession()
	server := jrpc2.NewServer(methods, &jrpc2.ServerOptions{
		Concurrency:    maxHandlers,
		DisableBuiltin: true,
	})
	server.Start(frames)
	return normalizeServeError(server.Wait())
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, errFrameTooLarge) || errors.Is(err, errInvalidUTF8) ||
		channel.IsErrClosing(err) {
		return nil
	}
	return err
}
