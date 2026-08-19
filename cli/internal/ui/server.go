package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

// PortStore is the remember/forget path the CLI already uses.
type PortStore interface {
	AddPort(port uint16) (bool, error)
	RemovePort(port uint16) (bool, error)
	Read() ([]core.ForwardingPolicy, error)
}

// Server is the loopback WebUI adapter: Snapshot/Watch plus policy writes.
type Server struct {
	Manager core.Manager
	Ports   PortStore
	Token   string
}

type portBody struct {
	Port uint16 `json:"port"`
}

type rememberedBody struct {
	RememberedPorts []uint16 `json:"remembered_ports"`
}

type rememberResult struct {
	Added bool   `json:"added"`
	Port  uint16 `json:"port"`
}

type forgetResult struct {
	Removed bool   `json:"removed"`
	Port    uint16 `json:"port"`
}

type errorBody struct {
	Error string `json:"error"`
}

// Handler serves the page and JSON API. Every request needs the token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/remembered", s.remembered)
	mux.HandleFunc("GET /api/watch", s.watch)
	mux.HandleFunc("POST /api/remember", s.remember)
	mux.HandleFunc("POST /api/forget", s.forget)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "missing or invalid token"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return false
	}
	return r.URL.Query().Get("token") == s.Token
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pageHTML)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := s.Manager.Snapshot(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	encoded, err := snapshot.Marshal(snap)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (s *Server) remembered(w http.ResponseWriter, r *http.Request) {
	policies, err := s.Ports.Read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rememberedBody{RememberedPorts: core.SimpleAutoForwardPorts(policies)})
}

func (s *Server) remember(w http.ResponseWriter, r *http.Request) {
	port, err := readPort(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
		return
	}
	changed, err := s.Ports.AddPort(port)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rememberResult{Added: changed, Port: port})
}

func (s *Server) forget(w http.ResponseWriter, r *http.Request) {
	port, err := readPort(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
		return
	}
	changed, err := s.Ports.RemovePort(port)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	if !changed {
		writeJSON(w, http.StatusNotFound, errorBody{Error: fmt.Sprintf("port %d is not remembered", port)})
		return
	}
	writeJSON(w, http.StatusOK, forgetResult{Removed: true, Port: port})
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "streaming unsupported"})
		return
	}
	stream, err := s.Manager.Watch(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		snap, err := stream.Next(r.Context())
		if err != nil {
			return
		}
		encoded, err := snapshot.Marshal(snap)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
			return
		}
		flusher.Flush()
	}
}

func readPort(r *http.Request) (uint16, error) {
	var body portBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("port is required")
	}
	if body.Port == 0 {
		return 0, fmt.Errorf("port must be 1..65535")
	}
	return body.Port, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// Serve runs the HTTP listener until ctx ends.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err := server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}
