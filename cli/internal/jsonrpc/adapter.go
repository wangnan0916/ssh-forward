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
	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

// ServeConn runs one JSON-RPC v1 session on conn until it ends.
func ServeConn(ctx context.Context, conn net.Conn, manager core.Manager) error {
	frames := newFrameChannel(conn, maxFrameBytes)
	// Negotiate before starting jrpc2 so pipelined or built-in methods cannot
	// overtake the session handshake.
	stopHandshake := context.AfterFunc(ctx, func() { _ = frames.Close() })
	capabilities, err := negotiateHello(frames)
	if err != nil {
		stopHandshake()
		return normalizeServeError(err)
	}
	if !stopHandshake() || ctx.Err() != nil {
		return nil
	}

	session := newConnectionSession(ctx, manager, capabilities)
	frames.bindPending(maxPendingCalls, session.onResponseSent)
	defer session.close()
	methods := handler.Map{
		methodSnapshot: func(ctx context.Context, request *jrpc2.Request) (any, error) {
			return handleSnapshot(ctx, request, manager)
		},
		methodWatch:   session.handleWatch,
		methodUnwatch: session.handleUnwatch,
	}
	stopSession := context.AfterFunc(ctx, func() { _ = frames.Close() })
	defer stopSession()
	server := jrpc2.NewServer(methods, &jrpc2.ServerOptions{
		AllowPush:      capabilities.watchSnapshot,
		Concurrency:    maxHandlers,
		DisableBuiltin: true,
		NewContext:     func() context.Context { return session.ctx },
	})
	session.server = server
	server.Start(frames)
	return normalizeServeError(server.Wait())
}

func marshalManagerError(err error) error {
	var domainError *core.DomainError
	if !errors.As(err, &domainError) {
		return internalError()
	}
	switch domainError.Kind {
	case core.ErrorManagerClosed:
		return (&jrpc2.Error{
			Code:    -32014,
			Message: "Manager is closed",
		}).WithData(errorData{Kind: string(domainError.Kind), Retryable: domainError.Retryable})
	case core.ErrorWatchLimit:
		return watchLimitError()
	default:
		return internalError()
	}
}

func handleSnapshot(ctx context.Context, request *jrpc2.Request, manager core.Manager) (any, error) {
	if err := parseSnapshotParams(request); err != nil {
		return nil, err
	}
	snap, err := manager.Snapshot(ctx)
	if err != nil {
		return nil, internalError()
	}
	return snapshotResult{Snapshot: snapshot.Encode(snap)}, nil
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
