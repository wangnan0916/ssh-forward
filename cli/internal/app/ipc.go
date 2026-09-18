package app

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	managerStatusPath      = "/v1/status"
	managerReloadPath      = "/v1/reload"
	managerProtocolVersion = 8
)

var ErrIncompatibleManager = errors.New("the running manager is incompatible")

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

type managerAllStatus struct {
	ProtocolVersion int           `json:"protocol_version"`
	ManagerVersion  string        `json:"manager_version"`
	Hosts           []core.Status `json:"hosts"`
}

func managerHandler(pool *managerPool, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == managerStatusPath:
			statuses, err := pool.AllStatuses(r.Context())
			if err != nil {
				http.Error(w, "manager unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(managerAllStatus{ProtocolVersion: managerProtocolVersion, ManagerVersion: version, Hosts: statuses})
		case r.Method == http.MethodPost && r.URL.Path == managerReloadPath:
			host := r.URL.Query().Get("host")
			if host != "" && !core.ValidHostName(host) {
				http.Error(w, "invalid host", http.StatusBadRequest)
				return
			}
			if err := pool.reload(r.Context(), host); err != nil {
				http.Error(w, "cannot reload configuration", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
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
