package app

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

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
