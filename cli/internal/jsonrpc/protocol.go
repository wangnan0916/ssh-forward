package jsonrpc

import (
	"github.com/creachadair/jrpc2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	protocolVersion = 3

	methodVersion = "system.version"
	methodStatus  = "manager.status"

	maxFrameBytes = 1 << 20
	maxHandlers   = 8
)

func internalError() *jrpc2.Error {
	return &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
}

type versionResult struct {
	Version int `json:"version"`
}

type statusResult struct {
	Status core.Status `json:"status"`
}
