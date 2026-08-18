package app

import (
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

// NewManager is the in-process Manager adapter: Connect and Serve use it
// after naming a Development Host, and integration tests construct it
// directly. It wires the production Local Endpoint allocator. The optional
// policySources argument is the Forwarding Policy seam (nil means unmatched
// listeners are not forwarded).
func NewManager(host core.HostAlias, connector core.HostConnector, policySources ...func() []core.ForwardingPolicy) core.Manager {
	var policies func() []core.ForwardingPolicy
	if len(policySources) != 0 {
		policies = policySources[0]
	}
	return core.NewConfiguredManager(host, connector, proxy.NewAllocator, policies)
}
