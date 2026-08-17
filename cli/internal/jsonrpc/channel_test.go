package jsonrpc

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBoundedLineChannelRecvRejectsOversizedFrame(t *testing.T) {
	payload := strings.Repeat("x", maxFrameBytes+1) + "\n"
	frames := newBoundedLineChannel(readCloser{Reader: strings.NewReader(payload)}, maxFrameBytes)
	_, err := frames.Recv()
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("Recv err = %v, want errFrameTooLarge", err)
	}
}

func TestBoundedLineChannelSendRejectsOversizedFrame(t *testing.T) {
	frames := newBoundedLineChannel(&writeCloser{}, maxFrameBytes)
	err := frames.Send(bytes.Repeat([]byte("x"), maxFrameBytes+1))
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("Send err = %v, want errFrameTooLarge", err)
	}
}

func TestBoundedLineChannelSendAppendsNewline(t *testing.T) {
	var buf writeCloser
	frames := newBoundedLineChannel(&buf, maxFrameBytes)
	if err := frames.Send([]byte(`{"jsonrpc":"2.0"}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if buf.String() != `{"jsonrpc":"2.0"}`+"\n" {
		t.Fatalf("wrote %q", buf.String())
	}
}

type readCloser struct{ io.Reader }

func (readCloser) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (readCloser) Close() error              { return nil }

type writeCloser struct{ bytes.Buffer }

func (*writeCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (*writeCloser) Close() error             { return nil }
