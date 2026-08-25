package app

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	managerStatusPath      = "/v1/status"
	managerIntentPath      = "/v1/intent"
	managerProtocolVersion = 4
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

type managerStatus struct {
	ProtocolVersion int    `json:"protocol_version"`
	ManagerVersion  string `json:"manager_version"`
	core.Status
}

func managerHandler(manager core.Manager, version string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == managerStatusPath:
			handleManagerStatus(writer, request, manager, version)
		case request.Method == http.MethodPut && request.URL.Path == managerIntentPath:
			handleManagerIntentUpdate(writer, request, manager)
		default:
			http.NotFound(writer, request)
		}
	})
}

func handleManagerStatus(writer http.ResponseWriter, request *http.Request, manager core.Manager, version string) {
	status, err := manager.Status(request.Context())
	if err != nil {
		http.Error(writer, "manager unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(managerStatus{
		ProtocolVersion: managerProtocolVersion,
		ManagerVersion:  version,
		Status:          status,
	})
}

func handleManagerIntentUpdate(writer http.ResponseWriter, request *http.Request, manager core.Manager) {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var intent core.ForwardingIntent
	if err := decoder.Decode(&intent); err != nil {
		http.Error(writer, "invalid forwarding intent", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(writer, "invalid forwarding intent", http.StatusBadRequest)
		return
	}
	if err := manager.UpdateIntent(request.Context(), intent); err != nil {
		http.Error(writer, "manager unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
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
