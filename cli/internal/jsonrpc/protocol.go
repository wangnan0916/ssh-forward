package jsonrpc

import (
	"github.com/creachadair/jrpc2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	protocolVersion = 2

	methodVersion        = "system.version"
	methodSnapshot       = "manager.snapshot"
	methodWatch          = "manager.watch"
	methodUnwatch        = "manager.unwatch"
	methodResyncRequired = "manager.resync_required"

	maxFrameBytes = 1 << 20
	maxHandlers   = 8
)

var errInvalidParameters = (&jrpc2.Error{
	Code:    jrpc2.InvalidParams,
	Message: "invalid parameters",
}).WithData(errorData{Kind: "invalid_parameters"})

func internalError() *jrpc2.Error {
	return &jrpc2.Error{Code: jrpc2.InternalError, Message: "internal error"}
}

func watchLimitError() *jrpc2.Error {
	return (&jrpc2.Error{
		Code:    -32015,
		Message: "too many active Watches",
	}).WithData(errorData{Kind: "watch_limit", Retryable: true})
}

type versionResult struct {
	Version int `json:"version"`
}

type snapshotResult struct {
	Snapshot core.Snapshot `json:"snapshot"`
}

type watchResult struct {
	WatchID  string        `json:"watch_id"`
	Snapshot core.Snapshot `json:"snapshot"`
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
	Snapshot core.Snapshot `json:"snapshot"`
}

type resyncNotification struct {
	WatchID string `json:"watch_id"`
	Reason  string `json:"reason"`
}

type errorData struct {
	Kind      string `json:"kind"`
	Retryable bool   `json:"retryable"`
}
