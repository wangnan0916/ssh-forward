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
	session := newConnectionSession(ctx, manager)
	defer session.close()
	methods := handler.Map{
		methodVersion: func(context.Context, *jrpc2.Request) (any, error) {
			return versionResult{Version: protocolVersion}, nil
		},
		methodSnapshot: func(ctx context.Context, _ *jrpc2.Request) (any, error) {
			return handleSnapshot(ctx, manager)
		},
		methodWatch:   session.handleWatch,
		methodUnwatch: session.handleUnwatch,
	}
	stopSession := context.AfterFunc(ctx, func() { _ = frames.Close() })
	defer stopSession()
	server := jrpc2.NewServer(methods, &jrpc2.ServerOptions{
		AllowPush:      true,
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

func handleSnapshot(ctx context.Context, manager core.Manager) (any, error) {
	snap, err := manager.Snapshot(ctx)
	if err != nil {
		return nil, internalError()
	}
	return snapshotResult{Snapshot: snapshot.Encode(snap)}, nil
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
