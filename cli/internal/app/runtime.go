package app

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

func inProcess(host, sshConfig, configPath, controlDirectory string) (core.Manager, error) {
	intent, err := HostIntent(configPath, host)
	if err != nil {
		return nil, err
	}
	adapter, err := NewOpenSSHAdapter(sshConfig, controlDirectory)
	if err != nil {
		return nil, err
	}
	return core.NewManager(core.HostAlias(host), adapter, intent), nil
}

func NewOpenSSHAdapter(sshConfig, controlDirectory string) (*openssh.Adapter, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("cannot find the OpenSSH client: %w", err)
	}
	options := openssh.Options{Executable: sshPath, ControlDirectory: controlDirectory}
	if sshConfig != "" {
		absolute, err := filepath.Abs(sshConfig)
		if err != nil {
			return nil, err
		}
		options.ConfigFile = absolute
	}
	return openssh.New(options)
}
