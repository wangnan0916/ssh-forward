package app

import (
	"os"
	"path/filepath"
	"runtime"
)

// Layout is the per-user product directory: Persistent intent files, the
// Manager singleton socket, and its pid/log sit together.
type Layout struct {
	Dir      string
	Config   string
	Policies string
	Socket   string
	PID      string
	Log      string
	UIPID    string
	UILog    string
	UIURL    string
}

// DefaultLayout resolves the product directory per cli-and-state.md:
// SSH_FORWARD_CONFIG_DIR overrides it, then the platform application-support
// locations.
func DefaultLayout() Layout {
	dir := configDir()
	return Layout{
		Dir:      dir,
		Config:   filepath.Join(dir, "config.jsonc"),
		Policies: filepath.Join(dir, "policies.jsonc"),
		Socket:   filepath.Join(dir, "manager.sock"),
		PID:      filepath.Join(dir, "manager.pid"),
		Log:      filepath.Join(dir, "manager.log"),
		UIPID:    filepath.Join(dir, "ui.pid"),
		UILog:    filepath.Join(dir, "ui.log"),
		UIURL:    filepath.Join(dir, "ui.url"),
	}
}

func configDir() string {
	if override := os.Getenv("SSH_FORWARD_CONFIG_DIR"); override != "" {
		return override
	}
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "ssh-forward")
	case "linux":
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(os.Getenv("HOME"), ".config")
		}
		base = filepath.Join(base, "ssh-forward")
	case "windows":
		base = filepath.Join(os.Getenv("APPDATA"), "ssh-forward")
	default:
		base = "."
	}
	return base
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
