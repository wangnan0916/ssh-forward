package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
)

type managerService interface {
	serviceUninstaller
	Start() error
	Install() error
}

func ensureService(svc managerService, layout Layout) error {
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

func installAndStart(svc managerService) error {
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

func reinstallService(svc managerService, layout Layout) error {
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
