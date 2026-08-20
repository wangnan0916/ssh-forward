package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/present"
)

// Intent is the WebUI's policy seam: read the effective set and remember
// or forget a port. Production uses app.FilePolicyReader; tests inject the
// same type or a fake.
type Intent interface {
	Effective() ([]core.ForwardingPolicy, bool, error)
	AddPort(uint16) (bool, error)
	RemovePort(uint16) (bool, error)
}

// Server is the loopback WebUI adapter: Snapshot/Watch plus policy writes.
type Server struct {
	Manager core.Manager
	Intent  Intent
	Token   string
	hub     *watchHub
}

type portBody struct {
	Port uint16 `json:"port"`
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

type viewDocument struct {
	Revision        core.Revision   `json:"revision"`
	Host            *present.Chrome `json:"host"`
	Lists           present.Lists   `json:"lists"`
	RememberedPorts []uint16        `json:"remembered_ports"`
}

// Handler serves the page and JSON API. Every request needs the
// capability token, as a query parameter or the host-only cookie.
func (s *Server) Handler() http.Handler {
	if s.hub == nil {
		s.hub = newWatchHub(s.Manager)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/watch", s.watch)
	mux.HandleFunc("POST /api/remember", s.remember)
	mux.HandleFunc("POST /api/forget", s.forget)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "missing or invalid token"})
			return
		}
		if r.URL.Query().Get("token") != "" {
			grantTokenCookie(w, s.Token)
		}
		mux.ServeHTTP(w, r)
	})
}

// Close ends the shared Manager Watch.
func (s *Server) Close() {
	if s.hub != nil {
		s.hub.stop()
	}
}

func (s *Server) authorized(r *http.Request) bool {
	return tokenEqual(tokenFromRequest(r), s.Token)
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
	writeJSON(w, http.StatusOK, s.view(snap))
}

func (s *Server) remember(w http.ResponseWriter, r *http.Request) {
	port, ok := s.decodePort(w, r)
	if !ok {
		return
	}
	changed, err := s.Intent.AddPort(port)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rememberResult{Added: changed, Port: port})
}

func (s *Server) forget(w http.ResponseWriter, r *http.Request) {
	port, ok := s.decodePort(w, r)
	if !ok {
		return
	}
	changed, err := s.Intent.RemovePort(port)
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
	initial, updates, unsub, err := s.hub.subscribe()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: err.Error()})
		return
	}
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	writeView := func(snap core.Snapshot) bool {
		encoded, err := json.Marshal(s.view(snap))
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if initial.Host != nil || initial.Revision != 0 {
		if !writeView(initial) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-updates:
			if !ok {
				return
			}
			if !writeView(snap) {
				return
			}
		}
	}
}

func (s *Server) view(snap core.Snapshot) viewDocument {
	policies, reliable, _ := s.Intent.Effective()
	operator := present.NewDocument(snap.Host, policies, reliable)
	doc := viewDocument{
		Revision:        snap.Revision,
		Lists:           operator.Lists,
		RememberedPorts: operator.Remembered,
	}
	if snap.Host != nil {
		chrome := operator.Chrome
		doc.Host = &chrome
	}
	return doc
}

func (s *Server) decodePort(w http.ResponseWriter, r *http.Request) (uint16, bool) {
	port, err := readPort(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
		return 0, false
	}
	if s.Intent == nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "no policies file is configured"})
		return 0, false
	}
	return port, true
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
