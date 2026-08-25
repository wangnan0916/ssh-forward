package app

import (
	"errors"
	"slices"
	"testing"

	"github.com/kardianos/service"
)

type fakeManagerService struct {
	status    service.Status
	statusErr error
	events    []string
}

func (s *fakeManagerService) Status() (service.Status, error) {
	return s.status, s.statusErr
}

func (s *fakeManagerService) Stop() error {
	s.events = append(s.events, "stop")
	return nil
}

func (s *fakeManagerService) Uninstall() error {
	s.events = append(s.events, "uninstall")
	return nil
}

func (s *fakeManagerService) Start() error {
	s.events = append(s.events, "start")
	return nil
}

func (s *fakeManagerService) Install() error {
	s.events = append(s.events, "install")
	return nil
}

func TestEnsureServiceInstallsAndStartsMissingManager(t *testing.T) {
	svc := &fakeManagerService{statusErr: service.ErrNotInstalled}
	if err := ensureService(svc, Layout{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	wantServiceEvents(t, svc.events, "install", "start")
}

func TestReinstallServiceReplacesRunningManager(t *testing.T) {
	svc := &fakeManagerService{status: service.StatusRunning}
	if err := reinstallService(svc, Layout{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	wantServiceEvents(t, svc.events, "stop", "uninstall", "install", "start")
}

func TestUninstallServiceStopsRunningManager(t *testing.T) {
	svc := &fakeManagerService{status: service.StatusRunning}
	if err := uninstallService(svc, Layout{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	wantServiceEvents(t, svc.events, "stop", "uninstall")
}

func TestUninstallServiceIsIdempotent(t *testing.T) {
	svc := &fakeManagerService{statusErr: service.ErrNotInstalled}
	if err := uninstallService(svc, Layout{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	wantServiceEvents(t, svc.events)
}

func TestUninstallServiceReportsStatusFailure(t *testing.T) {
	want := errors.New("status unavailable")
	svc := &fakeManagerService{statusErr: want}
	if err := uninstallService(svc, Layout{Dir: t.TempDir()}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func wantServiceEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}
