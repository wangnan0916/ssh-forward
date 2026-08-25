package openssh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

var ErrInvalidAlias = errors.New("invalid Development Host alias")

const maxStderrTailBytes = 64 << 10

type Options struct {
	Executable       string
	ConfigFile       string
	ControlDirectory string
	ReadyTimeout     time.Duration
	WaitDelay        time.Duration
}

// Adapter observes remote listeners and reconciles local and remote forwards
// through one product-private OpenSSH master per host. OpenSSH owns the
// forwarding data plane.
type Adapter struct {
	executable       string
	configFile       string
	controlDirectory string
	readyTimeout     time.Duration
	waitDelay        time.Duration
	environment      []string

	mu      sync.Mutex
	closed  bool
	masters map[core.HostAlias]*sshMaster
}

func New(options Options) (*Adapter, error) {
	if !filepath.IsAbs(options.Executable) {
		return nil, errors.New("OpenSSH executable path is not absolute")
	}
	if options.ConfigFile != "" && !filepath.IsAbs(options.ConfigFile) {
		return nil, errors.New("OpenSSH config path is not absolute")
	}
	if !filepath.IsAbs(options.ControlDirectory) {
		return nil, errors.New("OpenSSH control directory path is not absolute")
	}
	if err := os.MkdirAll(options.ControlDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create OpenSSH control directory: %w", err)
	}
	info, err := os.Stat(options.ControlDirectory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("OpenSSH control directory must not be writable by other users")
	}
	if options.ReadyTimeout <= 0 {
		options.ReadyTimeout = 10 * time.Second
	}
	if options.WaitDelay <= 0 {
		options.WaitDelay = 2 * time.Second
	}
	return &Adapter{
		executable:       options.Executable,
		configFile:       options.ConfigFile,
		controlDirectory: options.ControlDirectory,
		readyTimeout:     options.ReadyTimeout,
		waitDelay:        options.WaitDelay,
		environment:      approvedEnvironment(),
		masters:          make(map[core.HostAlias]*sshMaster),
	}, nil
}
