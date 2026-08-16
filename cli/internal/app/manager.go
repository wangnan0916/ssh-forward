package app

import (
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

// NewManager is the assembly point for the headless Manager: everything a
// runnable Manager needs beyond core itself is composed here. Its consumers
// today are the disposable-host integration tests (integration/sshhost);
// the CLI entry point is the planned second consumer and will land here,
// not in core. Slice 5 extends this seam with the Forwarding Policy source,
// One-time Approval storage, and Ask state before they reach
// core.NewConfiguredManager.
func NewManager(host core.HostAlias, adapter *openssh.Adapter) core.Manager {
	return core.NewConfiguredManager(host, adapter)
}
