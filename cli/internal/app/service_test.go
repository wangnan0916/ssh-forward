package app

import (
	"errors"
	"testing"

	"github.com/kardianos/service"
)

type fakeServiceUninstaller struct {
	status      service.Status
	statusErr   error
	stopped     bool
	uninstalled bool
}

func (s *fakeServiceUninstaller) Status() (service.Status, error) {
	return s.status, s.statusErr
}

func (s *fakeServiceUninstaller) Stop() error {
	s.stopped = true
	return nil
}

func (s *fakeServiceUninstaller) Uninstall() error {
	s.uninstalled = true
	return nil
}

func TestUninstallServiceStopsRunningManager(t *testing.T) {
	svc := &fakeServiceUninstaller{status: service.StatusRunning}
	if err := uninstallService(svc, Layout{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !svc.stopped || !svc.uninstalled {
		t.Fatalf("stopped = %v, uninstalled = %v", svc.stopped, svc.uninstalled)
	}
}

func TestUninstallServiceIsIdempotent(t *testing.T) {
	svc := &fakeServiceUninstaller{statusErr: service.ErrNotInstalled}
	if err := uninstallService(svc, Layout{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if svc.stopped || svc.uninstalled {
		t.Fatalf("stopped = %v, uninstalled = %v", svc.stopped, svc.uninstalled)
	}
}

func TestUninstallServiceReportsStatusFailure(t *testing.T) {
	want := errors.New("status unavailable")
	svc := &fakeServiceUninstaller{statusErr: want}
	if err := uninstallService(svc, Layout{Dir: t.TempDir()}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
