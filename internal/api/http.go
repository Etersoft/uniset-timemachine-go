package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"
)

// Server реализует HTTP API управления проигрывателем.
type Server struct {
	manager  *Manager
	mux      *http.ServeMux
	streamer *StateStreamer
}

//go:embed ui/*
var staticFS embed.FS

// NewServer создаёт HTTP сервер с зарегистрированными хендлерами.
func NewServer(manager *Manager, streamer *StateStreamer) *Server {
	uiFS, err := fs.Sub(staticFS, "ui")
	if err != nil {
		log.Fatalf("ui assets: %v", err)
	}
	s := &Server{
		manager:  manager,
		mux:      http.NewServeMux(),
		streamer: streamer,
	}
	s.routes(http.FS(uiFS))
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

func (s *Server) routes(uiFS http.FileSystem) {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	s.mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(uiFS)))
	s.mux.Handle("/ui/ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
	s.mux.Handle("/ui/ui/", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
	s.mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	apiRoutes := []struct {
		path    string
		handler http.Handler
	}{
		{"/api/v1/job", http.HandlerFunc(s.handleJob)},
		{"/api/v2/sensors", http.HandlerFunc(s.handleSensors)},
		{"/api/v2/job", http.HandlerFunc(s.handleJobV2)},
		{"/api/v2/job/range", http.HandlerFunc(s.handleSetRange)},
		{"/api/v2/job/seek", http.HandlerFunc(s.handleSetSeek)},
		{"/api/v2/job/start", http.HandlerFunc(s.handleStartPending)},
		{"/api/v2/job/pause", http.HandlerFunc(s.wrapSimpleWithLog("pause", s.manager.Pause))},
		{"/api/v2/job/resume", http.HandlerFunc(s.wrapSimpleWithLog("resume", s.manager.Resume))},
		{"/api/v2/job/stop", http.HandlerFunc(s.wrapSimpleWithLog("stop", s.manager.Stop))},
		{"/api/v2/job/apply", http.HandlerFunc(s.wrapSimpleWithLog("apply", s.manager.Apply))},
		{"/api/v2/job/step/forward", http.HandlerFunc(s.wrapSimpleWithLog("step_forward", s.manager.StepForward))},
		{"/api/v2/job/step/backward", http.HandlerFunc(s.handleStepBackward)},
		{"/api/v1/range", http.HandlerFunc(s.handleRange)},
		{"/api/v1/job/pause", http.HandlerFunc(s.wrapSimple(s.manager.Pause))},
		{"/api/v1/job/resume", http.HandlerFunc(s.wrapSimple(s.manager.Resume))},
		{"/api/v1/job/stop", http.HandlerFunc(s.wrapSimple(s.manager.Stop))},
		{"/api/v1/job/apply", http.HandlerFunc(s.wrapSimple(s.manager.Apply))},
		{"/api/v1/job/step/forward", http.HandlerFunc(s.wrapSimple(s.manager.StepForward))},
		{"/api/v1/job/step/backward", http.HandlerFunc(s.handleStepBackward)},
		{"/api/v1/job/seek", http.HandlerFunc(s.handleSeek)},
		{"/api/v1/job/state", http.HandlerFunc(s.handleState)},
		{"/api/v1/snapshot", http.HandlerFunc(s.handleSnapshot)},
		{"/api/v1/ws/state", http.HandlerFunc(s.handleWSState)},
	}
	for _, route := range apiRoutes {
		s.mux.Handle(route.path, s.withCORS(route.handler))
	}
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
		log.Printf("[http] job start from=%s to=%s step=%s speed=%f window=%s", from.Format(time.RFC3339), to.Format(time.RFC3339), step, req.Speed, window)
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

// handleJobV2 возвращает состояние и позволяет запустить через Pending.
func (s *Server) handleJobV2(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.manager.Status())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSensors возвращает список датчиков с именами (для подсказок в UI).
func (s *Server) handleSensors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var list []SensorInfo
	if s.streamer != nil {
		list = s.streamer.ListSensors()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sensors": list,
	})
}

// handleSetRange сохраняет параметры диапазона без старта задачи.
func (s *Server) handleSetRange(w http.ResponseWriter, r *http.Request) {
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
		if req.Speed <= 0 {
			req.Speed = 1
		}
		log.Printf("[http] set range v2 from=%s to=%s step=%s speed=%f window=%s", from.Format(time.RFC3339), to.Format(time.RFC3339), step, req.Speed, window)
		s.manager.SetRange(from, to, step, req.Speed, window)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case http.MethodGet:
		min, max, err := s.manager.Range(r.Context())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if min.IsZero() || max.IsZero() {
			writeError(w, http.StatusNotFound, fmt.Errorf("no data range found"))
			return
		}
		resp := map[string]string{
			"from": "",
			"to":   "",
		}
		if !min.IsZero() {
			resp["from"] = min.Format(time.RFC3339)
		}
		if !max.IsZero() {
			resp["to"] = max.Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSetSeek сохраняет отложенный seek.
func (s *Server) handleSetSeek(w http.ResponseWriter, r *http.Request) {
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
	log.Printf("[http] set seek v2 ts=%s apply=%t", ts.Format(time.RFC3339), req.Apply)
	if err := s.manager.Seek(ts, req.Apply); err != nil {
		if err.Error() == "no active job" || err.Error() == "job is already finished" {
			log.Printf("[http] set pending seek ts=%s (pending: %v)", ts.Format(time.RFC3339), err)
			s.manager.SetPendingSeek(ts)
			writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleStartPending запускает задачу из отложенного диапазона.
func (s *Server) handleStartPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := s.manager.StartPending(r.Context()); err != nil {
		code := http.StatusBadRequest
		if err.Error() == "job is already active" {
			code = http.StatusConflict
		}
		writeError(w, code, err)
		return
	}
	log.Printf("[http] start pending")
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
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
	log.Printf("[http] command step_backward apply=%t", req.Apply)
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
	log.Printf("[http] command seek ts=%s apply=%t", ts.Format(time.RFC3339), req.Apply)
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

func (s *Server) handleRange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	min, max, err := s.manager.Range(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resp := map[string]string{
		"from": "",
		"to":   "",
	}
	if !min.IsZero() {
		resp["from"] = min.Format(time.RFC3339)
	}
	if !max.IsZero() {
		resp["to"] = max.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWSState(w http.ResponseWriter, r *http.Request) {
	if s.streamer == nil {
		http.Error(w, "websocket streamer not configured", http.StatusServiceUnavailable)
		return
	}
	s.streamer.ServeWS(w, r)
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

func (s *Server) wrapSimpleWithLog(label string, fn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		log.Printf("[http] command %s", label)
		if err := fn(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
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
