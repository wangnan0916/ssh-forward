package app

import (
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

// NewManager is the assembly point for the headless Manager: everything a
// runnable Manager needs beyond core itself is composed here. Its consumers
// today are the disposable-host integration tests (integration/sshhost) and
// the CLI entry point; slice 5's Forwarding Policy source lands through the
// optional policySource argument (nil means default Ask).
func NewManager(host core.HostAlias, adapter *openssh.Adapter, policySources ...func() []core.ForwardingPolicy) core.Manager {
	var policies func() []core.ForwardingPolicy
	if len(policySources) != 0 {
		policies = policySources[0]
	}
	return core.NewConfiguredManager(host, adapter, policies)
}
