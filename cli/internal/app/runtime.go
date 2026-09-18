package app

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

func NewOpenSSHAdapter(sshConfig, controlDirectory, name string, target HostTarget) (*openssh.Adapter, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("cannot find the OpenSSH client: %w", err)
	}
	options := openssh.Options{Executable: sshPath, ControlDirectory: controlDirectory, Target: target.Target, Identity: name, Arguments: append([]string{"-o", "BatchMode=yes"}, target.Arguments...)}
	if sshConfig != "" {
		absolute, err := filepath.Abs(sshConfig)
		if err != nil {
			return nil, err
		}
		options.ConfigFile = absolute
	}
	return openssh.New(options)
}

func targetManager(name string, target HostTarget, intent core.ForwardingIntent, opts Options) (core.Manager, error) {
	if target.Diagnostic != "" {
		return core.NewManager(core.HostAlias(name), nil, intent), nil
	}
	adapter, err := NewOpenSSHAdapter(opts.SSHConfigPath, opts.Layout.Dir, name, target)
	if err != nil {
		return nil, err
	}
	return core.NewManager(core.HostAlias(name), adapter, intent), nil
}
