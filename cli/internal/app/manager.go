package app

import (
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

// NewManager is the assembly point for the headless Manager: everything a
// runnable Manager needs beyond core itself is composed here. Its consumers
// today are the disposable-host integration tests (integration/sshhost) and
// the CLI entry point. The optional policySources argument is the
// Forwarding Policy seam (nil means unmatched listeners are not forwarded).
func NewManager(host core.HostAlias, adapter *openssh.Adapter, policySources ...func() []core.ForwardingPolicy) core.Manager {
	var policies func() []core.ForwardingPolicy
	if len(policySources) != 0 {
		policies = policySources[0]
	}
	return core.NewConfiguredManager(host, adapter, policies)
}
