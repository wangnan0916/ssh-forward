package jsonrpc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

const outboundWriteTimeout = 5 * time.Second

var (
	errFrameTooLarge = errors.New("JSON-RPC frame exceeds maximum size")
	errInvalidUTF8   = errors.New("JSON-RPC frame is not valid UTF-8")
)

// frameChannel adapts a local byte stream to jrpc2's message channel. It only
// owns transport concerns that jrpc2's unbounded line channel cannot enforce.
type frameChannel struct {
	reader *bufio.Reader
	stream io.ReadWriteCloser
	max    int

	sendMu sync.Mutex

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

func (c *frameChannel) Send(message []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.writeFrame(message)
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
	message, err := c.readFrame()
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(message) {
		_ = c.Close()
		return nil, errInvalidUTF8
	}
	return message, nil
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

func (c *frameChannel) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.stream.Close()
	})
	return c.closeErr
}
