package app

import (
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

// NewManager is the assembly point for the headless Manager: everything a
// runnable Manager needs beyond core itself is composed here. Both the CLI
// entry point and the disposable-host integration tests consume it, so a
// change in composition lands in one place. Slice 5 extends this seam with
// the Forwarding Policy source, One-time Approval storage, and Ask state
// before they reach core.NewConfiguredManager.
func NewManager(host core.HostAlias, adapter *openssh.Adapter) core.Manager {
	return core.NewConfiguredManager(host, adapter)
}
