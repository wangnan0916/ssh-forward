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

type frameChannel struct {
	reader *bufio.Reader
	stream io.ReadWriteCloser
	max    int

	sendMu sync.Mutex

	slots      chan struct{}
	done       chan struct{}
	onResponse func(decodedResponse)

	closeOnce sync.Once
	closeErr  error
}

func newFrameChannel(stream io.ReadWriteCloser, maxBytes int) *frameChannel {
	return &frameChannel{
		reader: bufio.NewReader(stream),
		stream: stream,
		max:    maxBytes,
	}
}

func (c *frameChannel) bindPending(maxPending int, onResponse func(decodedResponse)) {
	c.slots = make(chan struct{}, maxPending)
	c.done = make(chan struct{})
	c.onResponse = onResponse
}

func (c *frameChannel) Send(message []byte) error {
	if c.slots == nil && c.onResponse == nil {
		c.sendMu.Lock()
		defer c.sendMu.Unlock()
		return c.writeFrame(message)
	}
	envelope, response := decodeResponseEnvelope(message)
	c.sendMu.Lock()
	err := c.writeFrame(message)
	c.sendMu.Unlock()
	if !response {
		return err
	}
	c.release()
	if err == nil && c.onResponse != nil {
		c.onResponse(envelope)
	}
	return err
}

func (c *frameChannel) writeFrame(message []byte) error {
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

func (c *frameChannel) Recv() ([]byte, error) {
	if err := c.acquire(); err != nil {
		return nil, err
	}
	message, err := c.readFrame()
	if err != nil {
		c.release()
		return nil, err
	}
	if !utf8.Valid(message) {
		c.release()
		return nil, c.reject(jrpc2.InvalidRequest, "frame is not valid UTF-8", errInvalidUTF8)
	}
	if trimmed := bytes.TrimSpace(message); len(trimmed) != 0 && trimmed[0] == '[' {
		c.release()
		return nil, c.reject(jrpc2.InvalidRequest, "batch requests are not supported", errBatchUnsupported)
	}
	if c.slots != nil && isNotification(message) {
		c.release()
		return nil, c.reject(jrpc2.InvalidRequest, "notifications are not negotiated", errNotificationRejected)
	}
	return message, nil
}

func (c *frameChannel) acquire() error {
	if c.slots == nil {
		return nil
	}
	select {
	case c.slots <- struct{}{}:
		return nil
	case <-c.done:
		return channel.ErrClosed
	}
}

func (c *frameChannel) readFrame() ([]byte, error) {
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

func (c *frameChannel) reject(code jrpc2.Code, message string, result error) error {
	return rejectAndClose(c, nil, code, message, nil, result)
}

func (c *frameChannel) Close() error {
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		c.closeErr = c.stream.Close()
	})
	return c.closeErr
}

func (c *frameChannel) release() {
	if c.slots == nil {
		return
	}
	select {
	case <-c.slots:
	default:
	}
}

type envelopeShape struct {
	ID      json.RawMessage
	Method  json.RawMessage
	Result  json.RawMessage
	Error   json.RawMessage
	JSONRPC string
	Params  json.RawMessage
	HasID   bool
}

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

type decodedResponse struct {
	ID     json.RawMessage
	Result json.RawMessage
}

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

func isNotification(message []byte) bool {
	shape, ok := decodeEnvelopeShape(message)
	if !ok {
		return false
	}
	return !shape.HasID || bytes.Equal(bytes.TrimSpace(shape.ID), []byte("null"))
}
