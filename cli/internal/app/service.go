package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

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

// Uninstall stops and removes the per-user Manager service. Persistent port
// configuration is deliberately kept so a later install can resume it.
func Uninstall(layout Layout) error {
	opts := Options{Layout: layout}.WithDefaults()
	svc, err := newManagerService(context.Background(), opts, "")
	if err != nil {
		return err
	}
	return uninstallService(svc, opts.Layout)
}

type serviceUninstaller interface {
	Status() (service.Status, error)
	Stop() error
	Uninstall() error
}

func uninstallService(svc serviceUninstaller, layout Layout) error {
	status, err := svc.Status()
	if errors.Is(err, service.ErrNotInstalled) {
		_, err = stopLegacyManager(layout)
		return err
	}
	if err != nil {
		return err
	}
	if status == service.StatusRunning {
		if err := svc.Stop(); err != nil {
			return err
		}
		waitSocketGone(layout.Socket, 2*time.Second)
	}
	if err := svc.Uninstall(); err != nil {
		return err
	}
	if !socketLive(layout.Socket) {
		_ = os.Remove(layout.Socket)
	}
	return nil
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
		Description:      "Keeps selected Development Host ports available on localhost.",
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
		Handler:           managerHandler(manager, p.opts.Version),
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

// stopLegacyManager is a one-way migration from v0.1. New Managers do not
// create or use PID files.
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
