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

type snapshotParams struct {
	Scope struct {
		Kind string `json:"kind"`
	} `json:"scope"`
}

type snapshotResult struct {
	Snapshot struct {
		Revision uint64 `json:"revision"`
	} `json:"snapshot"`
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

func negotiateHello(frames channel.Channel) error {
	message, err := frames.Recv()
	if err != nil {
		return err
	}
	if !json.Valid(message) {
		return rejectHandshake(frames, nil, jrpc2.ParseError, "parse error", nil)
	}
	request, ok := decodeRequestEnvelope(message)
	if !ok {
		return rejectHandshake(frames, nil, jrpc2.InvalidRequest, "invalid request", nil)
	}
	if request.Method != "system.hello" {
		return rejectHandshake(frames, request.ID, jrpc2.Code(-32001), "system.hello is required before manager methods", errorData{Kind: "hello_required"})
	}

	var params helloParams
	if len(request.Params) == 0 || json.Unmarshal(request.Params, &params) != nil || !validHelloParams(params) {
		return rejectHandshake(frames, request.ID, jrpc2.InvalidParams, "invalid parameters", errorData{Kind: "invalid_parameters"})
	}
	if *params.Protocol.Major != protocolMajor {
		return rejectHandshake(frames, request.ID, jrpc2.Code(-32002), "incompatible protocol major", incompatibleProtocolData{
			Kind:      "incompatible_protocol",
			Supported: protocolVersion{Major: protocolMajor, Minor: protocolMinor},
		})
	}
	if err := sendResult(frames, request.ID, helloResult{
		Protocol:      protocolVersion{Major: protocolMajor, Minor: protocolMinor},
		Capabilities:  make([]string, 0),
		MaxFrameBytes: maxFrameBytes,
	}); err != nil {
		_ = frames.Close()
		return err
	}
	return nil
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
