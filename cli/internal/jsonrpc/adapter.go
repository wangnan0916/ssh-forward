package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	"github.com/creachadair/jrpc2/handler"

	"ssh-forward/cli/internal/core"
)

func Serve(ctx context.Context, conn net.Conn, manager core.Manager) error {
	line := newBoundedLineChannel(conn, maxFrameBytes)
	frames := &validatingChannel{Channel: line}
	// Negotiate before starting jrpc2 so pipelined or built-in methods cannot
	// overtake the session handshake.
	stopHandshake := context.AfterFunc(ctx, func() { _ = frames.Close() })
	if err := negotiateHello(frames); err != nil {
		stopHandshake()
		return normalizeServeError(err)
	}
	if !stopHandshake() || ctx.Err() != nil {
		return nil
	}

	methods := handler.Map{
		"manager.snapshot": func(ctx context.Context, request *jrpc2.Request) (any, error) {
			return handleSnapshot(ctx, request, manager)
		},
	}
	pending := newPendingChannel(frames, maxPendingCalls)
	stopSession := context.AfterFunc(ctx, func() { _ = pending.Close() })
	defer stopSession()
	server := jrpc2.NewServer(methods, &jrpc2.ServerOptions{
		Concurrency:    maxHandlers,
		DisableBuiltin: true,
	}).Start(pending)
	return normalizeServeError(server.Wait())
}

func handleSnapshot(ctx context.Context, request *jrpc2.Request, manager core.Manager) (any, error) {
	var params snapshotParams
	if paramsText := request.ParamString(); paramsText == "" || json.Unmarshal([]byte(paramsText), &params) != nil {
		return nil, errInvalidParameters
	}
	if params.Scope.Kind != "all" {
		return nil, errInvalidScope
	}
	snapshot, err := manager.Snapshot(ctx, core.AllHosts())
	if err != nil {
		return nil, &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
	}
	var result snapshotResult
	result.Snapshot.Revision = uint64(snapshot.Revision)
	return result, nil
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, errFrameTooLarge) || errors.Is(err, errHandshakeRejected) ||
		errors.Is(err, errBatchUnsupported) || errors.Is(err, errInvalidUTF8) ||
		errors.Is(err, errNotificationRejected) || channel.IsErrClosing(err) {
		return nil
	}
	return err
}
