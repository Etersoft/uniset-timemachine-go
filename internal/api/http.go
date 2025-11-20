package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Server реализует HTTP API управления проигрывателем.
type Server struct {
	manager *Manager
	mux     *http.ServeMux
}

// NewServer создаёт HTTP сервер с зарегистрированными хендлерами.
func NewServer(manager *Manager) *Server {
	s := &Server{
		manager: manager,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

// Listen запускает сервер и блокируется до остановки.
func (s *Server) Listen(ctx context.Context, addr string) error {
	server := &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("/api/v1/job", s.handleJob)
	s.mux.HandleFunc("/api/v1/job/pause", s.wrapSimple(s.manager.Pause))
	s.mux.HandleFunc("/api/v1/job/resume", s.wrapSimple(s.manager.Resume))
	s.mux.HandleFunc("/api/v1/job/stop", s.wrapSimple(s.manager.Stop))
	s.mux.HandleFunc("/api/v1/job/apply", s.wrapSimple(s.manager.Apply))
	s.mux.HandleFunc("/api/v1/job/step/forward", s.wrapSimple(s.manager.StepForward))
	s.mux.HandleFunc("/api/v1/job/step/backward", s.handleStepBackward)
	s.mux.HandleFunc("/api/v1/job/seek", s.handleSeek)
	s.mux.HandleFunc("/api/v1/job/state", s.handleState)
	s.mux.HandleFunc("/api/v1/snapshot", s.handleSnapshot)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req startRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		from, err := time.Parse(time.RFC3339, req.From)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid from: %w", err))
			return
		}
		to, err := time.Parse(time.RFC3339, req.To)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid to: %w", err))
			return
		}
		step, err := time.ParseDuration(req.Step)
		if err != nil || step <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid step: %v", err))
			return
		}
		var window time.Duration
		if req.Window != "" {
			window, err = time.ParseDuration(req.Window)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid window: %v", err))
				return
			}
		}
		if err := s.manager.Start(r.Context(), from, to, step, req.Speed, window); err != nil {
			code := http.StatusBadRequest
			if err.Error() == "job is already active" {
				code = http.StatusConflict
			}
			writeError(w, code, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.manager.Status())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStepBackward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req applyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.manager.StepBackward(req.Apply); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleSeek(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req seekRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ts, err := time.Parse(time.RFC3339, req.TS)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid ts: %w", err))
		return
	}
	if err := s.manager.Seek(ts, req.Apply); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.manager.State())
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req snapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ts, err := time.Parse(time.RFC3339, req.TS)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid ts: %w", err))
		return
	}
	start := time.Now()
	if _, err := s.manager.Snapshot(r.Context(), ts); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ts":          ts.Format(time.RFC3339),
		"duration_ms": time.Since(start).Milliseconds(),
		"status":      "ok",
	})
}

func (s *Server) wrapSimple(fn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := fn(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

type startRequest struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Step   string  `json:"step"`
	Speed  float64 `json:"speed,omitempty"`
	Window string  `json:"window,omitempty"`
}

type applyRequest struct {
	Apply bool `json:"apply"`
}

type seekRequest struct {
	TS    string `json:"ts"`
	Apply bool   `json:"apply"`
}

type snapshotRequest struct {
	TS string `json:"ts"`
}

func decodeJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
