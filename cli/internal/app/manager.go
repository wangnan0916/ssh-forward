package app

import (
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

func NewManager(host core.HostAlias, adapter *openssh.Adapter) core.Manager {
	return core.NewConfiguredManager(host, adapter)
}
