package app

import (
	"context"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type targetBackend struct {
	core.Backend
	target core.HostAlias
}

func (b targetBackend) Observe(ctx context.Context, _ core.HostAlias, emit func([]core.Listener)) error {
	return b.Backend.Observe(ctx, b.target, emit)
}
func (b targetBackend) Forward(ctx context.Context, _ core.HostAlias, target core.ForwardTarget, ready func()) error {
	return b.Backend.Forward(ctx, b.target, target, ready)
}
func targetManager(name string, target HostTarget, intent core.ForwardingIntent, opts Options) (core.Manager, error) {
	if target.Diagnostic != "" {
		return core.NewManager(core.HostAlias(name), nil, intent), nil
	}
	adapter, err := NewOpenSSHAdapter(opts.SSHConfigPath, opts.Layout.Dir)
	if err != nil {
		return nil, err
	}
	adapter.SetControlIdentity(name)
	adapter.SetConnectionArguments(append([]string{"-o", "BatchMode=yes"}, target.Arguments...))
	return core.NewManager(core.HostAlias(name), targetBackend{Backend: adapter, target: core.HostAlias(target.Target)}, intent), nil
}

var _ core.Backend = targetBackend{}
