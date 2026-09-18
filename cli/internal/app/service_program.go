package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kardianos/service"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func newManagerService(ctx context.Context, opts Options, host string) (service.Service, error) {
	program := newManagerProgram(ctx, opts, host)
	config, err := serviceConfig(opts, host, program.wait)
	if err != nil {
		return nil, err
	}
	return service.New(program, config)
}

func serviceConfig(opts Options, host string, wait func()) (*service.Config, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve manager executable: %w", err)
	}
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
		Description:      "Keeps configured Development Hosts forwarding ports concurrently.",
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
	}, nil
}

type managerProgram struct {
	ctx           context.Context
	opts          Options
	host          string
	done          chan struct{}
	manager       *managerPool
	server        *http.Server
	listener      net.Listener
	cancelMonitor context.CancelFunc
	monitorDone   chan struct{}
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
	manager := &managerPool{
		configPath: p.opts.ConfigPath,
		managers:   make(map[string]core.Manager),
		createTarget: func(host string, target HostTarget, intent core.ForwardingIntent) (core.Manager, error) {
			return targetManager(host, target, intent, p.opts)
		},
	}
	_ = DiscoverHosts(p.ctx, p.opts.ConfigPath)
	if err := manager.reload(p.ctx, p.host); err != nil {
		_ = manager.Close(context.Background())
		_ = listener.Close()
		_ = os.Remove(p.opts.Layout.Socket)
		return err
	}
	monitorCtx, cancel := context.WithCancel(p.ctx)
	p.cancelMonitor = cancel
	p.monitorDone = make(chan struct{})
	go func() {
		defer close(p.monitorDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if err := DiscoverHosts(monitorCtx, p.opts.ConfigPath); err != nil && monitorCtx.Err() == nil {
				fmt.Fprintln(p.opts.Stderr, "Host discovery:", err)
			}
			if err := manager.reload(monitorCtx, ""); err != nil && monitorCtx.Err() == nil {
				fmt.Fprintln(p.opts.Stderr, "Reload hosts:", err)
			}
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
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
	if p.cancelMonitor != nil {
		p.cancelMonitor()
		<-p.monitorDone
	}
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
