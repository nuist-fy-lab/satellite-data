package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/pipeline"
	"github.com/nuist-fy-lab/satellite-data-downloader/internal/store"
)

type Server struct {
	store    *store.Store
	pipeline *pipeline.Runtime
}

func New(state *store.Store, runtime *pipeline.Runtime) http.Handler {
	s := &Server{store: state, pipeline: runtime}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/providers", s.providers)
	mux.HandleFunc("POST /v1/tasks", s.enqueue)
	mux.HandleFunc("GET /v1/tasks", s.tasks)
	mux.HandleFunc("POST /v1/tasks/status", s.taskStatuses)
	mux.HandleFunc("GET /v1/tasks/{id}", s.task)
	mux.HandleFunc("POST /v1/tasks/{id}/retry", s.retry)
	return withHeaders(mux)
}

func (s *Server) taskStatuses(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var payload struct {
		IDs []int64 `json:"ids"`
	}
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid task status JSON"))
		return
	}
	items, err := s.store.TasksByIDs(r.Context(), payload.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	items, err := s.pipeline.ProviderStatuses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var spec model.TaskSpec
	if err := decoder.Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid task JSON"))
		return
	}
	if strings.TrimSpace(spec.Key) == "" {
		spec.Key = taskKey(spec)
	}
	if err := s.pipeline.ValidateTask(spec); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "disabled") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	task, created, err := s.store.Enqueue(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"created": created, "task": task})
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.Tasks(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("state"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid task id"))
		return
	}
	item, err := s.store.Task(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": item})
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid task id"))
		return
	}
	if err := s.store.RetryTask(r.Context(), id); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	item, err := s.store.Task(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": item})
}

func taskKey(spec model.TaskSpec) string {
	hash := sha256.New()
	for _, value := range []string{spec.SourceID, spec.CollectionID, spec.GranuleID, spec.AssetID, spec.Name} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
