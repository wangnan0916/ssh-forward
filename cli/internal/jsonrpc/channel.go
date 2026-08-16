package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
)

const outboundWriteTimeout = 5 * time.Second

var errFrameTooLarge = errors.New("JSON-RPC frame exceeds maximum size")

type boundedLineChannel struct {
	reader *bufio.Reader
	stream io.ReadWriteCloser
	max    int

	closeOnce sync.Once
	closeErr  error
}

func newBoundedLineChannel(stream io.ReadWriteCloser, maxBytes int) *boundedLineChannel {
	return &boundedLineChannel{
		reader: bufio.NewReader(stream),
		stream: stream,
		max:    maxBytes,
	}
}

func (c *boundedLineChannel) Send(message []byte) error {
	if len(message) > c.max {
		_ = c.Close()
		return errFrameTooLarge
	}
	if bytes.IndexByte(message, '\n') >= 0 {
		_ = c.Close()
		return errors.New("JSON-RPC frame contains a newline")
	}
	frame := make([]byte, len(message)+1)
	copy(frame, message)
	frame[len(message)] = '\n'
	if connection, ok := c.stream.(interface{ SetWriteDeadline(time.Time) error }); ok {
		if err := connection.SetWriteDeadline(time.Now().Add(outboundWriteTimeout)); err != nil {
			_ = c.Close()
			return err
		}
		defer connection.SetWriteDeadline(time.Time{})
	}
	written, err := c.stream.Write(frame)
	if err != nil {
		_ = c.Close()
		return err
	}
	if written != len(frame) {
		_ = c.Close()
		return io.ErrShortWrite
	}
	return nil
}

func (c *boundedLineChannel) Recv() ([]byte, error) {
	var record []byte
	for {
		fragment, err := c.reader.ReadSlice('\n')
		hasDelimiter := len(fragment) != 0 && fragment[len(fragment)-1] == '\n'
		if hasDelimiter {
			fragment = fragment[:len(fragment)-1]
		}
		if len(fragment) > c.max-len(record) {
			_ = c.Close()
			return nil, errFrameTooLarge
		}
		record = append(record, fragment...)
		if hasDelimiter {
			return record, nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(record) == 0 {
				return nil, io.EOF
			}
			return nil, err
		}
	}
}

func (c *boundedLineChannel) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.stream.Close()
	})
	return c.closeErr
}

type serializedChannel struct {
	channel.Channel
	sendMu sync.Mutex
}

func (c *serializedChannel) Send(message []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.Channel.Send(message)
}

type validatingChannel struct {
	channel.Channel
}

func (c *validatingChannel) Recv() ([]byte, error) {
	message, err := c.Channel.Recv()
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(message) {
		return nil, c.reject(jrpc2.InvalidRequest, "frame is not valid UTF-8", errInvalidUTF8)
	}
	if trimmed := bytes.TrimSpace(message); len(trimmed) != 0 && trimmed[0] == '[' {
		return nil, c.reject(jrpc2.InvalidRequest, "batch requests are not supported", errBatchUnsupported)
	}
	return message, nil
}

func (c *validatingChannel) reject(code jrpc2.Code, message string, result error) error {
	return rejectAndClose(c.Channel, c.Channel.Close, nil, code, message, nil, result)
}

// envelopeShape is the JSON-RPC envelope decoded once: which members are
// present and their raw values. The directional classifiers — request,
// response, notification — layer their rules on this single shape statement,
// so the schema's conventions (the jsonrpc member, the id's null meaning)
// have one home instead of three partial map-decode copies.
type envelopeShape struct {
	ID      json.RawMessage
	Method  json.RawMessage
	Result  json.RawMessage
	Error   json.RawMessage
	JSONRPC string
	Params  json.RawMessage
	HasID   bool
}

// decodeEnvelopeShape decodes one frame into its member shape. Garbage or a
// bare null is not a shape; each classifier then applies its direction's
// rules.
func decodeEnvelopeShape(message []byte) (envelopeShape, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil || object == nil {
		return envelopeShape{}, false
	}
	shape := envelopeShape{
		ID:     object["id"],
		Method: object["method"],
		Result: object["result"],
		Error:  object["error"],
		Params: object["params"],
	}
	shape.HasID = object["id"] != nil
	if raw, found := object["jsonrpc"]; found {
		_ = json.Unmarshal(raw, &shape.JSONRPC)
	}
	return shape, true
}

// decodedResponse is the response frame decoded once by the channel's Send
// path and delivered to onResponse instead of raw bytes, so response
// structure knowledge lives in one place. Result carries the raw
// method-specific result, decoded by the session that owns the semantics.
type decodedResponse struct {
	ID     json.RawMessage
	Result json.RawMessage
}

// pendingChannel bounds jrpc2's inbound queue. It reports each successfully
// written response to onResponse (installed by the connection session, which
// owns any response-triggered activation); the channel itself is
// protocol-agnostic beyond the response/notification distinction required for
// slot release.
type pendingChannel struct {
	channel.Channel
	slots chan struct{}
	done  chan struct{}

	onResponse func(decodedResponse)

	closeOnce sync.Once
	closeErr  error
}

func newPendingChannel(base channel.Channel, maxPending int) *pendingChannel {
	return &pendingChannel{
		Channel: base,
		slots:   make(chan struct{}, maxPending),
		done:    make(chan struct{}),
	}
}

func (c *pendingChannel) Recv() ([]byte, error) {
	select {
	case c.slots <- struct{}{}:
	case <-c.done:
		return nil, channel.ErrClosed
	}
	message, err := c.Channel.Recv()
	if err != nil {
		c.release()
		return nil, err
	}
	if isNotification(message) {
		c.release()
		return nil, rejectAndClose(c.Channel, c.Close, nil, jrpc2.InvalidRequest, "notifications are not negotiated", nil, errNotificationRejected)
	}
	return message, nil
}

func (c *pendingChannel) Send(message []byte) error {
	envelope, response := decodeResponseEnvelope(message)
	err := c.Channel.Send(message)
	if !response {
		return err
	}
	c.release()
	if err == nil && c.onResponse != nil {
		c.onResponse(envelope)
	}
	return nil
}

// decodeResponseEnvelope decodes a frame once and classifies it: a response
// has an id and no method; anything else (request, notification, garbage) is
// not a response.
func decodeResponseEnvelope(message []byte) (decodedResponse, bool) {
	shape, ok := decodeEnvelopeShape(message)
	if !ok || shape.Method != nil || !shape.HasID {
		return decodedResponse{}, false
	}
	return decodedResponse{
		ID:     shape.ID,
		Result: shape.Result,
	}, true
}

func (c *pendingChannel) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.closeErr = c.Channel.Close()
	})
	return c.closeErr
}

func (c *pendingChannel) release() {
	select {
	case <-c.slots:
	default:
	}
}

func isNotification(message []byte) bool {
	shape, ok := decodeEnvelopeShape(message)
	if !ok {
		return false
	}
	return !shape.HasID || bytes.Equal(bytes.TrimSpace(shape.ID), []byte("null"))
}
