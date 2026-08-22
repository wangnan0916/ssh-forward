package app

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

func inProcess(host, sshConfig, configPath string) (core.Manager, error) {
	ports, err := Ports(configPath, host)
	if err != nil {
		return nil, err
	}
	adapter, err := NewOpenSSHAdapter(sshConfig)
	if err != nil {
		return nil, err
	}
	return core.NewManager(core.HostAlias(host), adapter, ports), nil
}

func NewOpenSSHAdapter(sshConfig string) (*openssh.Adapter, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("cannot find the OpenSSH client: %w", err)
	}
	options := openssh.Options{Executable: sshPath}
	if sshConfig != "" {
		absolute, err := filepath.Abs(sshConfig)
		if err != nil {
			return nil, err
		}
		options.ConfigFile = absolute
	}
	return openssh.New(options)
}
