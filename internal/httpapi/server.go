package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	appassets "youtubeshort"
	"youtubeshort/internal/config"
	"youtubeshort/internal/events"
	"youtubeshort/internal/model"
	"youtubeshort/internal/store"
)

type batchStarter interface {
	Start(batch model.Batch, req model.CreateBatchRequest)
	Stop(batchID int64) error
	Resume(batch model.Batch) error
}

type Server struct {
	cfg    config.App
	store  *store.Store
	runner batchStarter
	broker *events.Broker
}

func NewServer(cfg config.App, st *store.Store, runner batchStarter, broker *events.Broker) *Server {
	return &Server{
		cfg:    cfg,
		store:  st,
		runner: runner,
		broker: broker,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/batches", s.handleBatches)
	mux.HandleFunc("/api/batches/", s.handleBatchRoutes)
	mux.Handle("/", s.staticHandler())

	return withCORS(mux)
}

func (s *Server) staticHandler() http.Handler {
	staticDir := filepath.Clean(s.cfg.WebDistDir)
	diskRoot := http.Dir(staticDir)
	diskServer := http.FileServer(diskRoot)

	embeddedFS := appassets.WebDist()
	embeddedServer := http.FileServer(http.FS(embeddedFS))

	hasDiskIndex := fileExists(filepath.Join(staticDir, "index.html"))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		if hasDiskIndex {
			relative := strings.TrimPrefix(filepath.Clean(r.URL.Path), string(filepath.Separator))
			target := filepath.Join(staticDir, relative)
			if r.URL.Path == "/" || !fileExists(target) {
				http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
				return
			}
			diskServer.ServeHTTP(w, r)
			return
		}

		relative := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if r.URL.Path == "/" || !embeddedFileExists(embeddedFS, relative) {
			serveEmbeddedIndex(w, embeddedFS)
			return
		}
		embeddedServer.ServeHTTP(w, r)
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.store.GetSettings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		var settings model.Settings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(settings.ProviderOrder) == 0 {
			settings.ProviderOrder = []string{"chatgpt", "gemini", "copilot", "perplexity"}
		}
		if settings.DefaultMarket == "" {
			settings.DefaultMarket = "en"
		}
		if err := s.store.UpdateSettings(r.Context(), settings); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBatches(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := 12
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		batches, err := s.store.ListBatches(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, batches)
	case http.MethodPost:
		var req model.CreateBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(req.SeedKeywords) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("seedKeywords is required"))
			return
		}
		if req.PublishedWithinDays <= 0 {
			req.PublishedWithinDays = 90
		}
		if req.KeywordCap <= 0 {
			req.KeywordCap = 15
		}
		batch, err := s.store.CreateBatch(r.Context(), req, time.Now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.runner.Start(batch, req)
		writeJSON(w, http.StatusAccepted, batch)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBatchRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/batches/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	batchID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.handleBatchStatus(w, r, batchID)
		case http.MethodDelete:
			s.handleBatchDelete(w, r, batchID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	switch parts[1] {
	case "results":
		s.handleBatchResults(w, r, batchID)
	case "events":
		s.handleBatchEvents(w, r, batchID)
	case "resume":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.handleBatchResume(w, r, batchID)
	case "stop":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.handleBatchStop(w, r, batchID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request, batchID int64) {
	batch, err := s.store.GetBatch(r.Context(), batchID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	events, err := s.store.ListJobLogs(r.Context(), batchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, model.BatchStatusResponse{Batch: batch, Events: events})
}

func (s *Server) handleBatchResults(w http.ResponseWriter, r *http.Request, batchID int64) {
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortBy == "" {
		sortBy = "score"
	}
	results, err := s.store.ListBatchResults(r.Context(), batchID, sortBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request, batchID int64) {
	if err := s.store.DeleteBatch(r.Context(), batchID); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, err)
		case strings.Contains(err.Error(), "cannot delete running batch"):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBatchStop(w http.ResponseWriter, r *http.Request, batchID int64) {
	if err := s.runner.Stop(batchID); err != nil {
		switch {
		case strings.Contains(err.Error(), "not running"):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBatchResume(w http.ResponseWriter, r *http.Request, batchID int64) {
	batch, err := s.store.GetBatch(r.Context(), batchID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	status := strings.ToLower(strings.TrimSpace(batch.Status))
	if status != "stopped" && status != "failed" {
		writeError(w, http.StatusConflict, fmt.Errorf("cannot resume batch with status %q", batch.Status))
		return
	}
	if err := s.runner.Resume(batch); err != nil {
		switch {
		case strings.Contains(err.Error(), "cannot resume"):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	updated, err := s.store.GetBatch(r.Context(), batchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, updated)
}

func (s *Server) handleBatchEvents(w http.ResponseWriter, r *http.Request, batchID int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	existing, err := s.store.ListJobLogs(r.Context(), batchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, event := range existing {
		writeSSE(w, event)
	}
	flusher.Flush()

	sub, cancel := s.broker.Subscribe(batchID)
	defer cancel()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case event, ok := <-sub:
			if !ok {
				return
			}
			writeSSE(w, event)
			flusher.Flush()
		}
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeSSE(w http.ResponseWriter, event model.JobEvent) {
	data, _ := json.Marshal(event)
	fmt.Fprintf(w, "event: log\n")
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func embeddedFileExists(fsys fs.FS, name string) bool {
	if name == "" || name == "." {
		return false
	}
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

func serveEmbeddedIndex(w http.ResponseWriter, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "embedded index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
