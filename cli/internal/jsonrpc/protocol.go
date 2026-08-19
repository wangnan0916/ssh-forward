package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"

	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

const (
	protocolMajor = 1
	protocolMinor = 0

	// Wire method and notification names. Request-method keys (the handler
	// map in Serve) and notification-method names (the Watch fan-out) are
	// the same wire strings; named here so the two roles cannot drift.
	methodSnapshot       = "manager.snapshot"
	methodWatch          = "manager.watch"
	methodUnwatch        = "manager.unwatch"
	methodResyncRequired = "manager.resync_required"
	maxFrameBytes        = 1 << 20
	maxCapabilities      = 64
	maxCapabilitySize    = 128
	maxPendingCalls      = 64
	maxHandlers          = 8
	maxWatchID           = 64
	maxSessionWatches    = 8

	capabilityWatchSnapshot = "watch-snapshot-v1"
)

var (
	errHandshakeRejected    = errors.New("JSON-RPC handshake rejected")
	errBatchUnsupported     = errors.New("JSON-RPC batch requests are not supported")
	errInvalidUTF8          = errors.New("JSON-RPC frame is not valid UTF-8")
	errNotificationRejected = errors.New("JSON-RPC notifications are not negotiated")

	errInvalidParameters = (&jrpc2.Error{
		Code:    jrpc2.InvalidParams,
		Message: "invalid parameters",
	}).WithData(errorData{Kind: "invalid_parameters"})

	errInvalidScope = (&jrpc2.Error{
		Code:    jrpc2.InvalidParams,
		Message: "invalid parameters",
	}).WithData(errorData{Kind: "invalid_scope"})

	errWatchCapabilityRequired = (&jrpc2.Error{
		Code:    -32003,
		Message: "watch-snapshot-v1 capability is required",
	}).WithData(errorData{Kind: "capability_required"})

	errWatchLimit = watchLimitError()

	errHelloRequired = (&jrpc2.Error{
		Code:    -32001,
		Message: "system.hello is required before manager methods",
	}).WithData(errorData{Kind: "hello_required"})

	errIncompatibleProtocol = (&jrpc2.Error{
		Code:    -32002,
		Message: "incompatible protocol major",
	}).WithData(incompatibleProtocolData{
		Kind:      "incompatible_protocol",
		Supported: protocolVersion{Major: protocolMajor, Minor: protocolMinor},
	})
)

// internalError is the single construction of the wire internal-error shape.
// It deliberately carries no errorData Kind: it reports a server-side
// condition rather than a domain rejection, so clients cannot match or
// retry it against the domain vocabulary.
func internalError() *jrpc2.Error {
	return &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
}

// watchLimitError is the single construction of the Watch-limit wire error.
// Both limits produce it: the per-connection Watch slot cap enforced in the
// session and the Manager's global Watch cap translated from core, so the
// code, message, and retryable flag cannot drift between the two paths.
func watchLimitError() *jrpc2.Error {
	return (&jrpc2.Error{
		Code:    -32015,
		Message: "too many active Watches",
	}).WithData(errorData{Kind: "watch_limit", Retryable: true})
}

type protocolVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

type requestedProtocolVersion struct {
	Major *int `json:"major"`
	Minor *int `json:"minor"`
}

type helloParams struct {
	Protocol     *requestedProtocolVersion `json:"protocol"`
	Capabilities []string                  `json:"capabilities"`
}

type helloResult struct {
	Protocol      protocolVersion `json:"protocol"`
	Capabilities  []string        `json:"capabilities"`
	MaxFrameBytes int             `json:"max_frame_bytes"`
}

type negotiatedCapabilities struct {
	watchSnapshot bool
}

func negotiateCapabilities(requested []string) negotiatedCapabilities {
	var negotiated negotiatedCapabilities
	for _, capability := range requested {
		if capability == capabilityWatchSnapshot {
			negotiated.watchSnapshot = true
		}
	}
	return negotiated
}

func (c negotiatedCapabilities) wireValues() []string {
	capabilities := make([]string, 0, 1)
	if c.watchSnapshot {
		capabilities = append(capabilities, capabilityWatchSnapshot)
	}
	return capabilities
}

type snapshotParams struct {
	Scope struct {
		Kind string `json:"kind"`
	} `json:"scope"`
}

func allScopeParams() snapshotParams {
	var params snapshotParams
	params.Scope.Kind = "all"
	return params
}

func parseSnapshotParams(request *jrpc2.Request) error {
	paramsText := request.ParamString()
	if paramsText == "" || paramsText == "{}" {
		return nil
	}
	var params snapshotParams
	if json.Unmarshal([]byte(paramsText), &params) != nil {
		return errInvalidParameters
	}
	if params.Scope.Kind == "" || params.Scope.Kind == "all" {
		return nil
	}
	return errInvalidScope
}

type snapshotResult struct {
	Snapshot snapshot.Wire `json:"snapshot"`
}

type watchResult struct {
	WatchID  string        `json:"watch_id"`
	Snapshot snapshot.Wire `json:"snapshot"`
}

type unwatchParams struct {
	WatchID string `json:"watch_id"`
}

type unwatchResult struct {
	WatchID string `json:"watch_id"`
	Stopped bool   `json:"stopped"`
}

type snapshotNotification struct {
	WatchID  string        `json:"watch_id"`
	Snapshot snapshot.Wire `json:"snapshot"`
}

type resyncNotification struct {
	WatchID string `json:"watch_id"`
	Reason  string `json:"reason"`
}

type requestEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type responseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type errorData struct {
	Kind      string `json:"kind"`
	Retryable bool   `json:"retryable"`
}

type incompatibleProtocolData struct {
	Kind      string          `json:"kind"`
	Retryable bool            `json:"retryable"`
	Supported protocolVersion `json:"supported"`
}

func negotiateHello(frames channel.Channel) (negotiatedCapabilities, error) {
	message, err := frames.Recv()
	if err != nil {
		return negotiatedCapabilities{}, err
	}
	if !json.Valid(message) {
		return negotiatedCapabilities{}, rejectHandshake(frames, nil, jrpc2.ParseError, "parse error", nil)
	}
	request, ok := decodeRequestEnvelope(message)
	if !ok {
		return negotiatedCapabilities{}, rejectHandshake(frames, nil, jrpc2.InvalidRequest, "invalid request", nil)
	}
	if request.Method != "system.hello" {
		return negotiatedCapabilities{}, rejectHandshakeError(frames, request.ID, errHelloRequired)
	}

	var params helloParams
	if len(request.Params) == 0 || json.Unmarshal(request.Params, &params) != nil || !validHelloParams(params) {
		return negotiatedCapabilities{}, rejectHandshakeError(frames, request.ID, errInvalidParameters)
	}
	if *params.Protocol.Major != protocolMajor {
		return negotiatedCapabilities{}, rejectHandshakeError(frames, request.ID, errIncompatibleProtocol)
	}
	negotiated := negotiateCapabilities(params.Capabilities)
	if err := sendResult(frames, request.ID, helloResult{
		Protocol:      protocolVersion{Major: protocolMajor, Minor: protocolMinor},
		Capabilities:  negotiated.wireValues(),
		MaxFrameBytes: maxFrameBytes,
	}); err != nil {
		_ = frames.Close()
		return negotiatedCapabilities{}, err
	}
	return negotiated, nil
}

func decodeRequestEnvelope(message []byte) (requestEnvelope, bool) {
	shape, ok := decodeEnvelopeShape(message)
	if !ok {
		return requestEnvelope{}, false
	}
	if shape.Result != nil || shape.Error != nil {
		return requestEnvelope{}, false
	}
	// The jsonrpc member is enforced for inbound requests; the channel's
	// Send path decodes the same shape for outbound responses, which are
	// server-authored and always carry it.
	if shape.JSONRPC != "2.0" {
		return requestEnvelope{}, false
	}
	var method string
	if json.Unmarshal(shape.Method, &method) != nil || method == "" {
		return requestEnvelope{}, false
	}
	if !validRequestID(shape.ID) {
		return requestEnvelope{}, false
	}
	return requestEnvelope{
		JSONRPC: shape.JSONRPC,
		Method:  method,
		ID:      shape.ID,
		Params:  shape.Params,
	}, true
}

func validRequestID(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

func validHelloParams(params helloParams) bool {
	if params.Protocol == nil || params.Protocol.Major == nil || params.Protocol.Minor == nil ||
		*params.Protocol.Major < 0 || *params.Protocol.Minor < 0 || len(params.Capabilities) > maxCapabilities {
		return false
	}
	for _, capability := range params.Capabilities {
		if len(capability) == 0 || len(capability) > maxCapabilitySize {
			return false
		}
	}
	return true
}

func rejectHandshake(frames channel.Channel, id json.RawMessage, code jrpc2.Code, message string, data any) error {
	return rejectAndClose(frames, frames.Close, id, code, message, data, errHandshakeRejected)
}

// rejectAndClose sends a wire error, closes the channel through the given
// closer, and returns the caller's error. Handshake rejection, UTF-8/batch
// rejection, and inbound-notification rejection share this one
// close-after-error protocol.
func rejectAndClose(frames channel.Channel, closeChannel func() error, id json.RawMessage, code jrpc2.Code, message string, data any, result error) error {
	err := sendError(frames, id, code, message, data)
	_ = closeChannel()
	if err != nil {
		return err
	}
	return result
}

// rejectHandshakeError rejects the handshake with a prebuilt wire error, so
// the handshake path and the method path share one construction (the
// invalid-parameters sentinel) instead of re-typing its code, message, and
// kind literal.
func rejectHandshakeError(frames channel.Channel, id json.RawMessage, err *jrpc2.Error) error {
	return rejectAndClose(frames, frames.Close, id, err.Code, err.Message, err.Data, errHandshakeRejected)
}

func sendResult(frames channel.Channel, id json.RawMessage, result any) error {
	return sendEnvelope(frames, responseEnvelope{
		JSONRPC: "2.0",
		ID:      normalizeID(id),
		Result:  result,
	})
}

func sendError(frames channel.Channel, id json.RawMessage, code jrpc2.Code, message string, data any) error {
	return sendEnvelope(frames, responseEnvelope{
		JSONRPC: "2.0",
		ID:      normalizeID(id),
		Error: &wireError{
			Code:    int(code),
			Message: message,
			Data:    data,
		},
	})
}

func sendEnvelope(frames channel.Channel, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return frames.Send(encoded)
}

func normalizeID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
