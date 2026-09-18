package app

import (
	"errors"
	"testing"

	"github.com/kardianos/service"
	"github.com/stretchr/testify/require"
)

type fakeManagerService struct {
	status    service.Status
	statusErr error
	events    []string
}

func (s *fakeManagerService) Status() (service.Status, error) { return s.status, s.statusErr }
func (s *fakeManagerService) record(event string) error {
	s.events = append(s.events, event)
	return nil
}
func (s *fakeManagerService) Stop() error      { return s.record("stop") }
func (s *fakeManagerService) Start() error     { return s.record("start") }
func (s *fakeManagerService) Install() error   { return s.record("install") }
func (s *fakeManagerService) Uninstall() error { return s.record("uninstall") }

func TestServiceLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing bool
		run     func(*fakeManagerService, Layout) error
		events  []string
	}{
		{"install", true, func(s *fakeManagerService, l Layout) error { return ensureService(s, l) }, []string{"install", "start"}},
		{"replace", false, func(s *fakeManagerService, l Layout) error { return reinstallService(s, l) }, []string{"stop", "uninstall", "install", "start"}},
		{"uninstall", false, func(s *fakeManagerService, l Layout) error { return uninstallService(s, l) }, []string{"stop", "uninstall"}},
		{"idempotent uninstall", true, func(s *fakeManagerService, l Layout) error { return uninstallService(s, l) }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeManagerService{status: service.StatusRunning}
			if tc.missing {
				svc.statusErr = service.ErrNotInstalled
			}
			require.NoError(t, tc.run(svc, Layout{Dir: t.TempDir()}))
			require.Equal(t, tc.events, svc.events)
		})
	}
	unavailable := errors.New("status unavailable")
	require.ErrorIs(t, uninstallService(&fakeManagerService{statusErr: unavailable}, Layout{Dir: t.TempDir()}), unavailable)
}
