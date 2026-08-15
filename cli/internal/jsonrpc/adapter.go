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
		"manager.execute": func(ctx context.Context, request *jrpc2.Request) (any, error) {
			return handleExecute(ctx, request, manager)
		},
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

func handleExecute(ctx context.Context, request *jrpc2.Request, manager core.Manager) (any, error) {
	var params executeParams
	if paramsText := request.ParamString(); paramsText == "" || json.Unmarshal([]byte(paramsText), &params) != nil || len(params.Command) == 0 {
		return nil, errInvalidParameters
	}
	var header commandHeader
	if json.Unmarshal(params.Command, &header) != nil {
		return nil, errInvalidParameters
	}
	var command core.Command
	switch header.Kind {
	case "manual_forward.add":
		var add addManualForwardParams
		if json.Unmarshal(params.Command, &add) != nil || len(add.OperationID) == 0 || len(add.OperationID) > maxOperationID ||
			len(add.Host) == 0 || len(add.Host) > maxHostAlias || add.RemotePort == 0 ||
			(add.Family != string(core.FamilyAuto) && add.Family != string(core.FamilyIPv4) && add.Family != string(core.FamilyIPv6)) {
			return nil, errInvalidParameters
		}
		command = core.AddManualForward{
			CommandID:  core.CommandID(add.OperationID),
			Host:       core.HostAlias(add.Host),
			RemotePort: add.RemotePort,
			Family:     core.AddressFamily(add.Family),
		}
	case "manual_forward.remove":
		var remove removeForwardParams
		if json.Unmarshal(params.Command, &remove) != nil || len(remove.OperationID) == 0 || len(remove.OperationID) > maxOperationID ||
			len(remove.ForwardID) == 0 || len(remove.ForwardID) > maxForwardID {
			return nil, errInvalidParameters
		}
		command = core.RemoveForward{
			CommandID: core.CommandID(remove.OperationID),
			ForwardID: core.ForwardID(remove.ForwardID),
		}
	default:
		return nil, errInvalidParameters
	}
	outcome, err := manager.Execute(ctx, command)
	if err != nil {
		return nil, marshalManagerError(err)
	}
	return outcomeResult{Outcome: marshalOutcome(outcome)}, nil
}

func marshalManagerError(err error) error {
	var domainError *core.DomainError
	if !errors.As(err, &domainError) {
		return &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
	}
	code := jrpc2.Code(jrpc2.InternalError)
	message := "internal error"
	switch domainError.Kind {
	case core.ErrorInvalidCommand:
		code = jrpc2.InvalidParams
		message = "invalid parameters"
	case core.ErrorUnknownHost:
		code = -32010
		message = "unknown Development Host"
	case core.ErrorCommandIDConflict:
		code = -32011
		message = "operation ID conflicts with an earlier command"
	case core.ErrorLocalPortConflict:
		code = -32012
		message = "no permitted Local Endpoint port is available"
	case core.ErrorForwardNotFound:
		code = -32013
		message = "Forward was not found"
	case core.ErrorManagerClosed:
		code = -32014
		message = "Manager is closed"
	default:
		return &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
	}
	return (&jrpc2.Error{Code: code, Message: message}).WithData(errorData{
		Kind:      string(domainError.Kind),
		Retryable: domainError.Retryable,
	})
}

func marshalOutcome(outcome core.Outcome) wireOutcome {
	return wireOutcome{
		Kind:     string(outcome.Kind),
		Revision: uint64(outcome.Revision),
		Forward:  marshalForward(outcome.Forward),
	}
}

func marshalForward(forward core.ForwardSnapshot) wireForward {
	families := make([]string, len(forward.LocalFamilies))
	for index, family := range forward.LocalFamilies {
		families[index] = string(family)
	}
	return wireForward{
		ID:                 string(forward.ID),
		Kind:               string(forward.Kind),
		RemotePort:         forward.RemotePort,
		RemoteFamily:       string(forward.RemoteFamily),
		AllocatedLocalPort: forward.AllocatedLocalPort,
		LocalFamilies:      families,
	}
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
	hosts := make([]wireHost, len(snapshot.Hosts))
	for hostIndex, host := range snapshot.Hosts {
		forwards := make([]wireForward, len(host.Forwards))
		for forwardIndex, forward := range host.Forwards {
			forwards[forwardIndex] = marshalForward(forward)
		}
		hosts[hostIndex] = wireHost{
			Alias:      string(host.Alias),
			Connection: string(host.Connection),
			Forwards:   forwards,
		}
	}
	return snapshotResult{Snapshot: wireSnapshot{
		Revision: uint64(snapshot.Revision),
		Hosts:    hosts,
	}}, nil
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
