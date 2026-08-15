package app

import (
	"context"
	"errors"

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

func NewManager(host core.HostAlias, adapter *openssh.Adapter) core.Manager {
	return core.NewConfiguredManager(host, openSSHConnector{adapter: adapter})
}

type openSSHConnector struct {
	adapter *openssh.Adapter
}

func (c openSSHConnector) Connect(ctx context.Context, host core.HostAlias) (core.HostSession, error) {
	if err := c.adapter.ValidateAlias(ctx, string(host)); err != nil {
		if errors.Is(err, openssh.ErrInvalidAlias) {
			return nil, permanentConnectionError{err: err}
		}
		return nil, err
	}
	session, err := c.adapter.Start(ctx, string(host))
	if err != nil {
		var connectionError *openssh.ConnectionError
		if errors.As(err, &connectionError) &&
			(connectionError.Kind == openssh.ExitAuthentication || connectionError.Kind == openssh.ExitHostKey) {
			return nil, permanentConnectionError{err: err}
		}
		return nil, err
	}
	return openSSHSession{Session: session}, nil
}

type openSSHSession struct {
	*openssh.Session
}

func (s openSSHSession) RetryableExit() bool {
	switch s.ExitKind() {
	case openssh.ExitAuthentication, openssh.ExitHostKey, openssh.ExitCancelled:
		return false
	default:
		return true
	}
}

type permanentConnectionError struct {
	err error
}

func (e permanentConnectionError) Error() string {
	return e.err.Error()
}

func (permanentConnectionError) Retryable() bool {
	return false
}
