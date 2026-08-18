package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/ipc"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
	"github.com/wangnan0916/ssh-forward/cli/internal/proxy"
)

// Options configure Connect and Serve: the per-user layout, the Development
// Host naming inputs, and the streams used when attaching to a live
// singleton or prompting for a host.
type Options struct {
	Layout        Layout
	HostFlag      string
	SSHConfigPath string
	PoliciesPath  string
	ConfigPath    string
	Interactive   bool
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

// Session is one use of the per-user Manager: either a JSON-RPC client of
// the live singleton or an in-process Manager.
type Session struct {
	Manager      core.Manager
	Host         core.HostAlias
	PolicyReader *FilePolicyReader
}

func (o Options) WithDefaults() Options {
	if o.Layout.Dir == "" {
		o.Layout = DefaultLayout()
	}
	if o.PoliciesPath == "" {
		o.PoliciesPath = o.Layout.Policies
	}
	if o.ConfigPath == "" {
		o.ConfigPath = o.Layout.Config
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	return o
}

// Connect returns a Manager for this user: dial the live singleton, else
// auto-spawn it and dial, else (SSH_FORWARD_NO_AUTOSPAWN=1) build an
// in-process Manager. The caller owns Session.Manager and must Close it.
func Connect(ctx context.Context, opts Options) (Session, error) {
	opts = opts.WithDefaults()
	if client, err := ipc.Dial(ctx, opts.Layout.Socket); err == nil {
		return attach(ctx, client, opts)
	}

	host, err := ResolveHost(ResolveOptions{
		HostFlag:      opts.HostFlag,
		ConfigPath:    opts.ConfigPath,
		SSHConfigPath: opts.SSHConfigPath,
		Interactive:   opts.Interactive,
		Stdin:         opts.Stdin,
		Stdout:        opts.Stdout,
	})
	if err != nil {
		return Session{}, err
	}

	if os.Getenv("SSH_FORWARD_NO_AUTOSPAWN") != "1" {
		if err := spawn(opts, host); err != nil {
			return Session{}, fmt.Errorf("could not start the manager: %w", err)
		}
		if err := ipc.Wait(ctx, opts.Layout.Socket, 5*time.Second); err != nil {
			return Session{}, fmt.Errorf("manager did not start within %s (see %s)", 5*time.Second, opts.Layout.Log)
		}
		if client, err := ipc.Dial(ctx, opts.Layout.Socket); err == nil {
			return attach(ctx, client, opts)
		}
	}

	return inProcess(host, opts.SSHConfigPath, opts.PoliciesPath)
}

// Serve runs the per-user singleton in this process until ctx ends.
func Serve(ctx context.Context, opts Options) error {
	opts = opts.WithDefaults()
	host, err := ResolveHost(ResolveOptions{
		HostFlag:      opts.HostFlag,
		ConfigPath:    opts.ConfigPath,
		SSHConfigPath: opts.SSHConfigPath,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Layout.Dir, 0o700); err != nil {
		return err
	}
	endpoint, err := ipc.Listen(opts.Layout.Socket)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	if err := os.WriteFile(opts.Layout.PID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(opts.Layout.PID) }()
	session, err := inProcess(host, opts.SSHConfigPath, opts.PoliciesPath)
	if err != nil {
		return err
	}
	defer func() { _ = session.Manager.Close(context.Background()) }()
	return endpoint.Serve(ctx, session.Manager)
}

func attach(ctx context.Context, client core.Manager, opts Options) (Session, error) {
	snapshot, err := client.Snapshot(ctx)
	if err != nil || snapshot.Host == nil {
		_ = client.Close(context.Background())
		return Session{}, errors.New("the running manager has no Development Host configured")
	}
	if opts.HostFlag != "" && opts.HostFlag != string(snapshot.Host.Alias) {
		fmt.Fprintf(opts.Stderr, "ssh-forward: warning: --host %s ignored; the running manager owns %s\n", opts.HostFlag, snapshot.Host.Alias)
	}
	return Session{
		Manager:      client,
		Host:         snapshot.Host.Alias,
		PolicyReader: NewFilePolicyReader(opts.PoliciesPath),
	}, nil
}

func inProcess(host, sshConfig, policies string) (Session, error) {
	adapter, err := NewOpenSSHAdapter(sshConfig)
	if err != nil {
		return Session{}, err
	}
	reader := NewFilePolicyReader(policies)
	manager := core.NewConfiguredManager(core.HostAlias(host), adapter, proxy.NewAllocator, reader.Source())
	return Session{
		Manager:      manager,
		Host:         core.HostAlias(host),
		PolicyReader: reader,
	}, nil
}

func spawn(opts Options, host string) error {
	executable := os.Getenv("SSH_FORWARD_MANAGER_BINARY")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(opts.Layout.Dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(opts.Layout.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	args := []string{"--host", host, "--policies", opts.PoliciesPath, "manager", "serve"}
	if opts.SSHConfigPath != "" {
		args = append(args, "--ssh-config", opts.SSHConfigPath)
	}
	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	return nil
}

// NewOpenSSHAdapter constructs the OpenSSH adapter, resolving a relative
// config path to absolute (the adapter refuses relative ones).
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
