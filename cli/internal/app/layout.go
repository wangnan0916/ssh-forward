package app

import (
	"os"
	"path/filepath"
)

// Layout is the per-user persistent config and Manager socket.
type Layout struct {
	Dir    string
	Config string
	Socket string
}

// DefaultLayout resolves the product directory per cli-and-state.md:
// SSH_FORWARD_CONFIG_DIR overrides it, then the platform application-support
// locations.
func DefaultLayout() Layout {
	return layoutForDir(configDir())
}

func layoutForDir(dir string) Layout {
	return Layout{
		Dir:    dir,
		Config: filepath.Join(dir, "config.jsonc"),
		Socket: filepath.Join(dir, "manager.sock"),
	}
}

func configDir() string {
	if override := os.Getenv("SSH_FORWARD_CONFIG_DIR"); override != "" {
		return override
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	return filepath.Join(base, "ssh-forward")
}

// DefaultSSHConfigPath is the user's SSH client configuration. The OpenSSH
// adapter still lets ssh read it natively; this path is for host naming.
func DefaultSSHConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// SSHConfigPath returns flag when set, otherwise DefaultSSHConfigPath.
func SSHConfigPath(flag string) string {
	if flag != "" {
		return flag
	}
	return DefaultSSHConfigPath()
}
