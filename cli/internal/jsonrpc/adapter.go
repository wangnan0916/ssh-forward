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

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func Serve(ctx context.Context, conn net.Conn, manager core.Manager) error {
	line := newBoundedLineChannel(conn, maxFrameBytes)
	serialized := &serializedChannel{Channel: line}
	frames := &validatingChannel{Channel: serialized}
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

	pending := newPendingChannel(frames, maxPendingCalls)
	session := newConnectionSession(ctx, manager, capabilities)
	pending.onResponse = session.onResponseSent
	defer session.close()
	methods := handler.Map{
		methodSnapshot: func(ctx context.Context, request *jrpc2.Request) (any, error) {
			return handleSnapshot(ctx, request, manager)
		},
		methodWatch:   session.handleWatch,
		methodUnwatch: session.handleUnwatch,
	}
	stopSession := context.AfterFunc(ctx, func() { _ = pending.Close() })
	defer stopSession()
	server := jrpc2.NewServer(methods, &jrpc2.ServerOptions{
		AllowPush:      capabilities.watchSnapshot,
		Concurrency:    maxHandlers,
		DisableBuiltin: true,
		NewContext:     func() context.Context { return session.ctx },
	})
	session.server = server
	server.Start(pending)
	return normalizeServeError(server.Wait())
}

func marshalManagerError(err error) error {
	var domainError *core.DomainError
	if !errors.As(err, &domainError) {
		return internalError()
	}
	switch domainError.Kind {
	case core.ErrorLocalPortConflict:
		return (&jrpc2.Error{
			Code:    -32012,
			Message: "no permitted Local Endpoint port is available",
		}).WithData(errorData{Kind: string(domainError.Kind), Retryable: domainError.Retryable})
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
	snapshot, err := manager.Snapshot(ctx)
	if err != nil {
		return nil, internalError()
	}
	return snapshotResult{Snapshot: marshalSnapshot(snapshot)}, nil
}

// MarshalSnapshot encodes a Snapshot in the wire shape (the same shape
// manager.snapshot returns over JSON-RPC), so CLI --json output and the IPC
// protocol stay one contract for script and desktop clients.
func MarshalSnapshot(snapshot core.Snapshot) ([]byte, error) {
	return json.Marshal(marshalSnapshot(snapshot))
}

func marshalSnapshot(snapshot core.Snapshot) wireSnapshot {
	if snapshot.Host == nil {
		return wireSnapshot{Revision: uint64(snapshot.Revision)}
	}
	host := snapshot.Host
	forwards := make([]wireForward, len(host.Forwards))
	for forwardIndex, forward := range host.Forwards {
		forwards[forwardIndex] = marshalForward(forward)
	}
	observations := make([]wireListenerObservation, len(host.ListenerObservations))
	for observationIndex, observation := range host.ListenerObservations {
		observations[observationIndex] = marshalListenerObservation(observation)
	}
	lifetimes := make([]wireListenerLifetime, len(host.ListenerLifetimes))
	for lifetimeIndex, lifetime := range host.ListenerLifetimes {
		lifetimes[lifetimeIndex] = wireListenerLifetime{
			Family:       string(lifetime.Family),
			BindScope:    string(lifetime.BindScope),
			RemotePort:   lifetime.RemotePort,
			Status:       string(lifetime.Status),
			PostBaseline: lifetime.PostBaseline,
		}
	}
	return wireSnapshot{
		Revision: uint64(snapshot.Revision),
		Host: &wireHost{
			Alias:                string(host.Alias),
			Connection:           string(host.Connection),
			Discovery:            marshalDiscovery(host.Discovery),
			ListenerObservations: observations,
			ListenerLifetimes:    lifetimes,
			Forwards:             forwards,
		},
	}
}

func marshalDiscovery(discovery core.DiscoverySnapshot) wireDiscovery {
	return wireDiscovery{
		State: string(discovery.State),
		Capability: wireDiscoveryCapability{
			RemoteListeners: string(discovery.Capability.RemoteListeners),
			SocketIdentity:  string(discovery.Capability.SocketIdentity),
			ProcessMetadata: string(discovery.Capability.ProcessMetadata),
		},
		BaselineEstablished: discovery.BaselineEstablished,
		ScannerVersion:      discovery.ScannerVersion,
		ScannerChecksum:     discovery.ScannerChecksum,
		Diagnostic:          discovery.Diagnostic,
	}
}

func marshalListenerObservation(observation core.ListenerObservation) wireListenerObservation {
	identities := make([]string, len(observation.SocketIdentities))
	for index, identity := range observation.SocketIdentities {
		identities[index] = string(identity)
	}
	chains := make([]wireProcessChain, len(observation.Processes))
	for chainIndex, chain := range observation.Processes {
		processes := make([]wireProcessMetadata, len(chain.Processes))
		for processIndex, process := range chain.Processes {
			arguments := make([]string, len(process.Arguments))
			copy(arguments, process.Arguments)
			processes[processIndex] = wireProcessMetadata{
				PID:              process.PID,
				Executable:       process.Executable,
				WorkingDirectory: process.WorkingDirectory,
				Arguments:        arguments,
			}
		}
		chains[chainIndex] = wireProcessChain{Processes: processes}
	}
	return wireListenerObservation{
		Family:           string(observation.Family),
		BindScope:        string(observation.BindScope),
		RemotePort:       observation.RemotePort,
		SocketIdentities: identities,
		ProcessChains:    chains,
	}
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
