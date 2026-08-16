package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
)

const (
	protocolMajor     = 1
	protocolMinor     = 0
	maxFrameBytes     = 1 << 20
	maxCapabilities   = 64
	maxCapabilitySize = 128
	maxPendingCalls   = 64
	maxHandlers       = 8
	maxOperationID    = 128
	maxHostAlias      = 255
	maxForwardID      = 256
	maxWatchID        = 64
	maxSessionWatches = 8

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

	errWatchLimit = (&jrpc2.Error{
		Code:    -32015,
		Message: "too many active Watches",
	}).WithData(errorData{Kind: "watch_limit", Retryable: true})
)

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

type executeParams struct {
	Command json.RawMessage `json:"command"`
}

type commandHeader struct {
	Kind string `json:"kind"`
}

type addManualForwardParams struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	Host        string `json:"host"`
	RemotePort  uint16 `json:"remote_port"`
	Family      string `json:"family"`
}

type removeForwardParams struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	ForwardID   string `json:"forward_id"`
}

type outcomeResult struct {
	Outcome wireOutcome `json:"outcome"`
}

type wireOutcome struct {
	Kind     string      `json:"kind"`
	Revision uint64      `json:"revision"`
	Forward  wireForward `json:"forward"`
}

type wireForward struct {
	ID                 string   `json:"id"`
	Kind               string   `json:"kind"`
	RemotePort         uint16   `json:"remote_port"`
	RemoteFamily       string   `json:"remote_family"`
	AllocatedLocalPort uint16   `json:"allocated_local_port"`
	LocalFamilies      []string `json:"local_families"`
}

type snapshotParams struct {
	Scope struct {
		Kind string `json:"kind"`
	} `json:"scope"`
}

type snapshotResult struct {
	Snapshot wireSnapshot `json:"snapshot"`
}

type watchResult struct {
	WatchID  string       `json:"watch_id"`
	Snapshot wireSnapshot `json:"snapshot"`
}

type unwatchParams struct {
	WatchID string `json:"watch_id"`
}

type unwatchResult struct {
	WatchID string `json:"watch_id"`
	Stopped bool   `json:"stopped"`
}

type snapshotNotification struct {
	WatchID  string       `json:"watch_id"`
	Snapshot wireSnapshot `json:"snapshot"`
}

type resyncNotification struct {
	WatchID string `json:"watch_id"`
	Reason  string `json:"reason"`
}

type wireSnapshot struct {
	Revision uint64    `json:"revision"`
	Host     *wireHost `json:"host,omitempty"`
}

type wireHost struct {
	Alias                string                    `json:"alias"`
	Connection           string                    `json:"connection"`
	Discovery            wireDiscovery             `json:"discovery"`
	ListenerObservations []wireListenerObservation `json:"listener_observations"`
	Forwards             []wireForward             `json:"forwards"`
}

type wireDiscovery struct {
	State               string                  `json:"state"`
	Capability          wireDiscoveryCapability `json:"capability"`
	BaselineEstablished bool                    `json:"baseline_established"`
	ScannerVersion      int                     `json:"scanner_version"`
	ScannerChecksum     string                  `json:"scanner_checksum"`
	Diagnostic          string                  `json:"diagnostic"`
}

type wireDiscoveryCapability struct {
	RemoteListeners string `json:"remote_listeners"`
	SocketIdentity  string `json:"socket_identity"`
	ProcessMetadata string `json:"process_metadata"`
}

type wireListenerObservation struct {
	Family           string             `json:"family"`
	BindScope        string             `json:"bind_scope"`
	RemotePort       uint16             `json:"remote_port"`
	SocketIdentities []string           `json:"socket_identities"`
	ProcessChains    []wireProcessChain `json:"process_chains"`
}

type wireProcessChain struct {
	Processes []wireProcessMetadata `json:"processes"`
}

type wireProcessMetadata struct {
	PID              int      `json:"pid"`
	Executable       string   `json:"executable"`
	WorkingDirectory string   `json:"working_directory"`
	Arguments        []string `json:"arguments"`
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
		return negotiatedCapabilities{}, rejectHandshake(frames, request.ID, jrpc2.Code(-32001), "system.hello is required before manager methods", errorData{Kind: "hello_required"})
	}

	var params helloParams
	if len(request.Params) == 0 || json.Unmarshal(request.Params, &params) != nil || !validHelloParams(params) {
		return negotiatedCapabilities{}, rejectHandshake(frames, request.ID, jrpc2.InvalidParams, "invalid parameters", errorData{Kind: "invalid_parameters"})
	}
	if *params.Protocol.Major != protocolMajor {
		return negotiatedCapabilities{}, rejectHandshake(frames, request.ID, jrpc2.Code(-32002), "incompatible protocol major", incompatibleProtocolData{
			Kind:      "incompatible_protocol",
			Supported: protocolVersion{Major: protocolMajor, Minor: protocolMinor},
		})
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message, &fields); err != nil || fields == nil {
		return requestEnvelope{}, false
	}
	if _, found := fields["result"]; found {
		return requestEnvelope{}, false
	}
	if _, found := fields["error"]; found {
		return requestEnvelope{}, false
	}
	var request requestEnvelope
	if json.Unmarshal(fields["jsonrpc"], &request.JSONRPC) != nil || request.JSONRPC != "2.0" {
		return requestEnvelope{}, false
	}
	if json.Unmarshal(fields["method"], &request.Method) != nil || request.Method == "" {
		return requestEnvelope{}, false
	}
	request.ID = fields["id"]
	if !validRequestID(request.ID) {
		return requestEnvelope{}, false
	}
	request.Params = fields["params"]
	return request, true
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
	err := sendError(frames, id, code, message, data)
	_ = frames.Close()
	if err != nil {
		return err
	}
	return errHandshakeRejected
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

func sendEnvelope(frames channel.Channel, response responseEnvelope) error {
	encoded, err := json.Marshal(response)
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
