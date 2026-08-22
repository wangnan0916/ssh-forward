package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/jsonrpc"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

// HostPicker chooses one Development Host alias from a candidate list.
// ResolveHost uses it when more than one SSH alias is configured and none
// is pinned.
type HostPicker func(hosts []string, stdin io.Reader, stdout io.Writer) (string, error)

// Options configure Connect and Serve: the per-user layout, the Development
// Host naming inputs, and the streams used when attaching to a live
// singleton or prompting for a host.
type Options struct {
	Layout        Layout
	HostFlag      string
	SSHConfigPath string
	ConfigPath    string
	Interactive   bool
	PickHost      HostPicker
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

func (o Options) WithDefaults() Options {
	if o.Layout.Dir == "" {
		o.Layout = DefaultLayout()
	} else {
		if o.Layout.Config == "" {
			o.Layout.Config = filepath.Join(o.Layout.Dir, "config.jsonc")
		}
		if o.Layout.Socket == "" {
			o.Layout.Socket = filepath.Join(o.Layout.Dir, "manager.sock")
		}
		if o.Layout.PID == "" {
			o.Layout.PID = filepath.Join(o.Layout.Dir, "manager.pid")
		}
		if o.Layout.Log == "" {
			o.Layout.Log = filepath.Join(o.Layout.Dir, "manager.log")
		}
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

var (
	// ErrNotRunning reports that this user's manager pid/socket is not live.
	ErrNotRunning = errors.New("manager is not running")
	// ErrIncompatibleManager reports that a live singleton does not speak
	// this client's protocol. Callers must not spawn a second manager.
	ErrIncompatibleManager = errors.New("the running manager is incompatible; restart it with: ssh-forward manager restart")
)

// Connect returns a Manager for this user: dial the live singleton, else
// auto-spawn it and dial, else (SSH_FORWARD_NO_AUTOSPAWN=1) build an
// in-process Manager. The caller owns the returned Manager and must Close it.
func Connect(ctx context.Context, opts Options) (core.Manager, error) {
	opts = opts.WithDefaults()
	client, err := jsonrpc.Dial(ctx, opts.Layout.Socket)
	if err == nil {
		return attach(ctx, client, opts)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if jsonrpc.Live(opts.Layout.Socket) {
		return nil, ErrIncompatibleManager
	}

	host, err := ResolveHost(opts)
	if err != nil {
		return nil, err
	}

	if os.Getenv("SSH_FORWARD_NO_AUTOSPAWN") != "1" {
		pid, err := spawn(opts, host)
		if err != nil {
			return nil, fmt.Errorf("could not start the manager: %w", err)
		}
		timeout := 5 * time.Second
		if err := waitReady(ctx, timeout, pid, func() bool { return jsonrpc.Live(opts.Layout.Socket) },
			func() error { return startError(opts.Layout.Log, "manager failed to start") },
			func() error {
				return startError(opts.Layout.Log, fmt.Sprintf("manager did not start within %s", timeout))
			},
		); err != nil {
			return nil, err
		}
		if client, err := jsonrpc.Dial(ctx, opts.Layout.Socket); err == nil {
			return attach(ctx, client, opts)
		}
	}

	return inProcess(host, opts.SSHConfigPath, opts.ConfigPath)
}

// Stop ends this user's manager if we wrote its pid and it still answers
// the singleton socket. A live pid without that socket is left alone.
func Stop(layout Layout) error {
	if layout.Dir == "" {
		layout = DefaultLayout()
	}
	pid, err := requireLivePID(layout.PID, ErrNotRunning, func() { _ = os.Remove(layout.PID) })
	if err != nil {
		return err
	}
	if !jsonrpc.Live(layout.Socket) {
		return ErrNotRunning
	}
	if err := TerminatePID(pid); err != nil {
		return fmt.Errorf("stop manager: %w", err)
	}
	waitWhile(2*time.Second, func() bool { return jsonrpc.Live(layout.Socket) })
	_ = os.Remove(layout.PID)
	return nil
}

func waitWhile(timeout time.Duration, still func() bool) {
	deadline := time.Now().Add(timeout)
	for still() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

func waitReady(ctx context.Context, timeout time.Duration, childPID int, ready func() bool, onDead, onTimeout func() error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		if childPID > 0 && !PIDAlive(childPID) {
			return onDead()
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return onTimeout()
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func requireLivePID(path string, missing error, onDead func()) (int, error) {
	pid, err := ReadPIDFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, missing
		}
		return 0, err
	}
	if !PIDAlive(pid) {
		if onDead != nil {
			onDead()
		}
		return 0, missing
	}
	return pid, nil
}

func writePIDFile(path string) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}

// ReadPIDFile reads a decimal pid from path.
func ReadPIDFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid pid file")
	}
	return pid, nil
}

// PIDAlive reports whether pid still exists.
func PIDAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TerminatePID sends SIGTERM, waits up to 3s, then SIGKILL if needed.
func TerminatePID(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	waitWhile(3*time.Second, func() bool { return PIDAlive(pid) })
	if PIDAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// Serve runs the per-user singleton in this process until ctx ends.
func Serve(ctx context.Context, opts Options) error {
	opts = opts.WithDefaults()
	host, err := ResolveHost(opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Layout.Dir, 0o700); err != nil {
		return err
	}
	endpoint, err := jsonrpc.Listen(opts.Layout.Socket)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	if err := writePIDFile(opts.Layout.PID); err != nil {
		return err
	}
	defer func() { _ = os.Remove(opts.Layout.PID) }()
	manager, err := inProcess(host, opts.SSHConfigPath, opts.ConfigPath)
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close(context.Background()) }()
	return endpoint.Serve(ctx, manager)
}

func attach(ctx context.Context, client core.Manager, opts Options) (core.Manager, error) {
	status, err := client.Status(ctx)
	if err != nil {
		_ = client.Close(context.Background())
		return nil, fmt.Errorf("could not read the running manager: %w", err)
	}
	if status.Host == "" {
		_ = client.Close(context.Background())
		return nil, errors.New("the running manager has no Development Host configured")
	}
	if opts.HostFlag != "" && opts.HostFlag != string(status.Host) {
		fmt.Fprintf(opts.Stderr, "ssh-forward: warning: --host %s ignored; the running manager owns %s. Switch with: ssh-forward manager restart\n", opts.HostFlag, status.Host)
	}
	return client, nil
}

func inProcess(host, sshConfig, configPath string) (core.Manager, error) {
	adapter, err := NewOpenSSHAdapter(sshConfig)
	if err != nil {
		return nil, err
	}
	manager := core.NewManager(core.HostAlias(host), adapter, func() ([]uint16, error) {
		return Ports(configPath, host)
	})
	return manager, nil
}

const (
	envManagerServe     = "SSH_FORWARD_MANAGER_SERVE"
	envManagerHost      = "SSH_FORWARD_MANAGER_HOST"
	envManagerSSHConfig = "SSH_FORWARD_MANAGER_SSH_CONFIG"
	envManagerConfig    = "SSH_FORWARD_MANAGER_CONFIG"
	envConfigDir        = "SSH_FORWARD_CONFIG_DIR"
)

// TakeManagerServeEnv reports whether this process is the autospawned
// singleton child and copies its Options encoding into opts. The child
// enters Serve without parsing a Cobra command tree.
func TakeManagerServeEnv(opts *Options) bool {
	if os.Getenv(envManagerServe) != "1" {
		return false
	}
	_ = os.Unsetenv(envManagerServe)
	opts.HostFlag = os.Getenv(envManagerHost)
	if config := os.Getenv(envManagerConfig); config != "" {
		opts.ConfigPath = config
	}
	if sshConfig := os.Getenv(envManagerSSHConfig); sshConfig != "" {
		opts.SSHConfigPath = sshConfig
	}
	return true
}

func spawn(opts Options, host string) (int, error) {
	executable, err := ResolveSpawnBinary("SSH_FORWARD_MANAGER_BINARY")
	if err != nil {
		return 0, err
	}
	extra := []string{
		envManagerServe + "=1",
		envManagerHost + "=" + host,
		envManagerConfig + "=" + opts.ConfigPath,
		envConfigDir + "=" + opts.Layout.Dir,
	}
	if opts.SSHConfigPath != "" {
		extra = append(extra, envManagerSSHConfig+"="+opts.SSHConfigPath)
	}
	return StartDetached(executable, nil, extra, opts.Layout.Dir, opts.Layout.Log)
}

func startError(logPath, fallback string) error {
	if line := lastLogLine(logPath); line != "" {
		return fmt.Errorf("%s: %s (see %q)", fallback, line, logPath)
	}
	return fmt.Errorf("%s (see %q)", fallback, logPath)
}

func lastLogLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return strings.TrimPrefix(line, "ssh-forward: ")
		}
	}
	return ""
}

// ResolveSpawnBinary is the test override for a background child: env if
// set, otherwise this executable. `go test` must not spawn the test binary.
func ResolveSpawnBinary(envName string) (string, error) {
	if executable := os.Getenv(envName); executable != "" {
		return executable, nil
	}
	return os.Executable()
}

// StartDetached launches executable in a new session, appending stdout and
// stderr to logPath. extraEnv is added to the process environment. The
// returned pid is reaped in the background so a child that exits is not a
// zombie that still looks alive.
func StartDetached(executable string, args, extraEnv []string, dir, logPath string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	command := exec.Command(executable, args...)
	command.Env = append(os.Environ(), extraEnv...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	go func() { _ = command.Wait() }()
	return command.Process.Pid, nil
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
