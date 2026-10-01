package app

import (
	"context"
	"errors"
	"os"
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
	if err := uninstallService(svc, layout); err != nil {
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
		return nil
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
