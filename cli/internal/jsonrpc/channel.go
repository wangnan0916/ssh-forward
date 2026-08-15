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
	err := sendError(c.Channel, nil, code, message, nil)
	_ = c.Channel.Close()
	if err != nil {
		return err
	}
	return result
}

// pendingChannel bounds jrpc2's inbound queue and activates each Watch only
// after the response that introduces its server-assigned ID is written.
type pendingChannel struct {
	channel.Channel
	slots chan struct{}
	done  chan struct{}

	hooksMu    sync.Mutex
	watchHooks map[string]func()

	closeOnce sync.Once
	closeErr  error
}

func newPendingChannel(base channel.Channel, maxPending int) *pendingChannel {
	return &pendingChannel{
		Channel:    base,
		slots:      make(chan struct{}, maxPending),
		done:       make(chan struct{}),
		watchHooks: make(map[string]func()),
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
		err := sendError(c.Channel, nil, jrpc2.InvalidRequest, "notifications are not negotiated", nil)
		_ = c.Close()
		if err != nil {
			return nil, err
		}
		return nil, errNotificationRejected
	}
	return message, nil
}

func (c *pendingChannel) Send(message []byte) error {
	response := isResponse(message)
	err := c.Channel.Send(message)
	if !response {
		return err
	}
	c.release()
	if err != nil {
		return err
	}
	if watchID, ok := watchResponseID(message); ok {
		c.fireWatchResponseHook(watchID)
	}
	return nil
}

func (c *pendingChannel) afterWatchResponse(watchID string, hook func()) {
	c.hooksMu.Lock()
	defer c.hooksMu.Unlock()
	c.watchHooks[watchID] = hook
}

func (c *pendingChannel) fireWatchResponseHook(watchID string) {
	c.hooksMu.Lock()
	hook := c.watchHooks[watchID]
	delete(c.watchHooks, watchID)
	c.hooksMu.Unlock()
	if hook != nil {
		hook()
	}
}

func isResponse(message []byte) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil {
		return false
	}
	if _, request := object["method"]; request {
		return false
	}
	_, found := object["id"]
	return found
}

func watchResponseID(message []byte) (string, bool) {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(message, &envelope) != nil || len(envelope.Result) == 0 {
		return "", false
	}
	var result struct {
		WatchID  string          `json:"watch_id"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(envelope.Result, &result) != nil || result.WatchID == "" || len(result.Snapshot) == 0 {
		return "", false
	}
	return result.WatchID, true
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
	var object map[string]json.RawMessage
	if json.Unmarshal(message, &object) != nil {
		return false
	}
	id, hasID := object["id"]
	return !hasID || bytes.Equal(bytes.TrimSpace(id), []byte("null"))
}
