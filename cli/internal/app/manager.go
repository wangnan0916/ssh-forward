package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
)

const (
	managerServiceName = "com.wangnan0916.ssh-forward"
	managerStatusPath  = "/v1/status"
	managerStartWait   = 5 * time.Second
)

var (
	ErrNotRunning          = errors.New("manager is not running")
	ErrIncompatibleManager = errors.New("the running manager is incompatible; restart it with: ssh-forward manager restart")
)

// HostPicker chooses one Development Host alias from a candidate list.
type HostPicker func(hosts []string, stdin io.Reader, stdout io.Writer) (string, error)

// Options are the local files, host inputs, and command streams used by the
// CLI and its per-user Manager.
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

// Connect returns the running per-user Manager. The first command installs
// and starts it through the OS service manager; later calls only use HTTP over
// its user-only Unix socket.
func Connect(ctx context.Context, opts Options) (core.Manager, error) {
	opts = opts.WithDefaults()
	client, dialErr := dialManager(ctx, opts.Layout.Socket)
	if dialErr == nil {
		return attach(ctx, client, opts)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	host, err := ResolveHost(opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Layout.Dir, 0o700); err != nil {
		return nil, err
	}

	svc, err := newManagerService(ctx, opts, host)
	if err != nil {
		return nil, err
	}
	if errors.Is(dialErr, ErrIncompatibleManager) {
		err = reinstallService(svc, opts.Layout)
	} else {
		err = ensureService(svc, opts.Layout)
	}
	if err != nil {
		return nil, fmt.Errorf("could not start the manager: %w", err)
	}
	client, err = waitManager(ctx, opts.Layout.Socket, managerStartWait)
	if err != nil {
		return nil, err
	}
	return attach(ctx, client, opts)
}

// Stop asks the user's OS service manager to stop the Manager.
func Stop(layout Layout) error {
	opts := Options{Layout: layout}.WithDefaults()
	svc, err := newManagerService(context.Background(), opts, "")
	if err != nil {
		return err
	}
	status, err := svc.Status()
	if errors.Is(err, service.ErrNotInstalled) {
		if stopped, legacyErr := stopLegacyManager(opts.Layout); legacyErr != nil {
			return legacyErr
		} else if stopped {
			return nil
		}
		return ErrNotRunning
	}
	if err != nil {
		return err
	}
	if status != service.StatusRunning {
		return ErrNotRunning
	}
	if err := svc.Stop(); err != nil {
		return fmt.Errorf("stop manager: %w", err)
	}
	waitSocketGone(opts.Layout.Socket, 2*time.Second)
	return nil
}

// Restart replaces the installed definition with one for the current binary
// and host selection, then returns the ready Manager.
func Restart(ctx context.Context, opts Options) (core.Manager, error) {
	opts = opts.WithDefaults()
	host, err := ResolveHost(opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Layout.Dir, 0o700); err != nil {
		return nil, err
	}
	svc, err := newManagerService(ctx, opts, host)
	if err != nil {
		return nil, err
	}
	if err := reinstallService(svc, opts.Layout); err != nil {
		return nil, err
	}
	return waitManager(ctx, opts.Layout.Socket, managerStartWait)
}

// Serve runs the Manager in the current process. OS service definitions invoke
// this command; it is also useful in the foreground for diagnosis.
func Serve(ctx context.Context, opts Options) error {
	opts = opts.WithDefaults()
	host, err := ResolveHost(opts)
	if err != nil {
		return err
	}
	svc, err := newManagerService(ctx, opts, host)
	if err != nil {
		return err
	}
	return svc.Run()
}

func ensureService(svc service.Service, layout Layout) error {
	status, err := svc.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		if _, err := stopLegacyManager(layout); err != nil {
			return err
		}
		return installAndStart(svc)
	case err != nil:
		return err
	case status == service.StatusRunning:
		return nil
	case status == service.StatusStopped:
		return reinstallService(svc, layout)
	default:
		return errors.New("manager service status is unknown")
	}
}

func installAndStart(svc service.Service) error {
	if err := svc.Install(); err != nil {
		status, statusErr := svc.Status()
		if statusErr != nil {
			return err
		}
		if status == service.StatusRunning {
			return nil
		}
	}
	if err := svc.Start(); err != nil {
		status, _ := svc.Status()
		if status != service.StatusRunning {
			return err
		}
	}
	return nil
}

func reinstallService(svc service.Service, layout Layout) error {
	status, err := svc.Status()
	if err == nil {
		if status == service.StatusRunning {
			if err := svc.Stop(); err != nil {
				return err
			}
			waitSocketGone(layout.Socket, 2*time.Second)
		}
		if err := svc.Uninstall(); err != nil {
			return err
		}
	} else if !errors.Is(err, service.ErrNotInstalled) {
		return err
	}
	if _, err := stopLegacyManager(layout); err != nil {
		return err
	}
	return installAndStart(svc)
}

func newManagerService(ctx context.Context, opts Options, host string) (service.Service, error) {
	program := newManagerProgram(ctx, opts, host)
	return service.New(program, serviceConfig(opts, host, program.wait))
}

func serviceConfig(opts Options, host string, wait func()) *service.Config {
	executable, _ := os.Executable()
	arguments := []string{"manager", "serve"}
	if host != "" {
		arguments = append(arguments, "--host", host)
	}
	if opts.SSHConfigPath != "" {
		sshConfig, err := filepath.Abs(opts.SSHConfigPath)
		if err == nil {
			arguments = append(arguments, "--ssh-config", sshConfig)
		}
	}
	return &service.Config{
		Name:             managerServiceName,
		DisplayName:      "ssh-forward manager",
		Description:      "Keeps remembered Development Host ports available on localhost.",
		Executable:       executable,
		Arguments:        arguments,
		WorkingDirectory: opts.Layout.Dir,
		EnvVars:          map[string]string{"SSH_FORWARD_CONFIG_DIR": opts.Layout.Dir},
		Option: service.KeyValue{
			"UserService":  true,
			"KeepAlive":    true,
			"RunAtLoad":    true,
			"Restart":      "always",
			"LogOutput":    true,
			"LogDirectory": opts.Layout.Dir,
			"RunWait":      wait,
		},
	}
}

type managerProgram struct {
	ctx      context.Context
	opts     Options
	host     string
	done     chan struct{}
	manager  core.Manager
	server   *http.Server
	listener net.Listener
}

func newManagerProgram(ctx context.Context, opts Options, host string) *managerProgram {
	return &managerProgram{ctx: ctx, opts: opts, host: host, done: make(chan struct{})}
}

func (p *managerProgram) Start(service.Service) error {
	if err := os.MkdirAll(p.opts.Layout.Dir, 0o700); err != nil {
		return err
	}
	listener, err := listenManager(p.opts.Layout.Socket)
	if err != nil {
		return err
	}
	manager, err := inProcess(p.host, p.opts.SSHConfigPath, p.opts.ConfigPath)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(p.opts.Layout.Socket)
		return err
	}
	p.listener = listener
	p.manager = manager
	p.server = &http.Server{
		Handler:           managerHandler(manager),
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		_ = p.server.Serve(listener)
		close(p.done)
	}()
	return nil
}

func (p *managerProgram) wait() {
	select {
	case <-p.ctx.Done():
	case <-p.done:
	}
}

func (p *managerProgram) Stop(service.Service) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var err error
	if p.server != nil {
		err = p.server.Shutdown(ctx)
	}
	if p.listener != nil {
		_ = p.listener.Close()
		_ = os.Remove(p.opts.Layout.Socket)
	}
	if p.manager != nil {
		err = errors.Join(err, p.manager.Close(ctx))
	}
	return err
}

func listenManager(path string) (net.Listener, error) {
	if socketLive(path) {
		return nil, errors.New("manager is already running")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return listener, nil
}

func managerHandler(manager core.Manager) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != managerStatusPath {
			http.NotFound(writer, request)
			return
		}
		status, err := manager.Status(request.Context())
		if err != nil {
			http.Error(writer, "manager unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(status)
	})
}

type managerClient struct {
	client    *http.Client
	transport *http.Transport
}

func dialManager(ctx context.Context, socket string) (core.Manager, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: 2 * time.Second,
	}
	client := &managerClient{client: &http.Client{Transport: transport}, transport: transport}
	if _, err := client.Status(ctx); err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return client, nil
}

func (c *managerClient) Status(ctx context.Context) (core.Status, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://manager"+managerStatusPath, nil)
	if err != nil {
		return core.Status{}, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return core.Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return core.Status{}, ErrIncompatibleManager
	}
	if response.StatusCode != http.StatusOK {
		return core.Status{}, fmt.Errorf("manager status: %s", response.Status)
	}
	var status core.Status
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&status); err != nil {
		return core.Status{}, err
	}
	return status, nil
}

func (c *managerClient) Close(context.Context) error {
	c.transport.CloseIdleConnections()
	return nil
}

func waitManager(ctx context.Context, socket string, timeout time.Duration) (core.Manager, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		client, err := dialManager(ctx, socket)
		if err == nil {
			return client, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("manager did not become ready within %s: %w", timeout, lastErr)
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
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
	return core.NewManager(core.HostAlias(host), adapter, func() ([]uint16, error) {
		return Ports(configPath, host)
	}), nil
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

func socketLive(path string) bool {
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func waitSocketGone(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for socketLive(path) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// stopLegacyManager is a one-way migration from pre-service releases. New
// Managers do not create or use PID files.
func stopLegacyManager(layout Layout) (bool, error) {
	if !socketLive(layout.Socket) {
		_ = os.Remove(filepath.Join(layout.Dir, "manager.pid"))
		return false, nil
	}
	raw, err := os.ReadFile(filepath.Join(layout.Dir, "manager.pid"))
	if err != nil {
		return false, ErrIncompatibleManager
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false, ErrIncompatibleManager
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return false, err
	}
	waitSocketGone(layout.Socket, 2*time.Second)
	if socketLive(layout.Socket) {
		return false, ErrIncompatibleManager
	}
	_ = os.Remove(filepath.Join(layout.Dir, "manager.pid"))
	return true, nil
}
