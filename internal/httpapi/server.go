package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	webui "github.com/nuist-fy-lab/satellite-data/web"
)

type Server struct {
	registry *provider.Registry
}

func New(registry *provider.Registry) http.Handler {
	s := &Server{registry: registry}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/sources", s.sources)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/search", s.search)

	static, err := fs.Sub(webui.Files, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(static))
	return withHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}

func (s *Server) sources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": s.registry.Sources()})
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	sourceID := strings.TrimSpace(r.URL.Query().Get("source"))
	p, err := s.registry.Provider(sourceID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	collections, err := p.Collections(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":      p.Source(),
		"collections": collections,
	})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	sourceID := strings.TrimSpace(values.Get("source"))
	collectionID := strings.TrimSpace(values.Get("collection"))
	start, err := parseTime(values.Get("start"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid start time; use RFC3339 or YYYY-MM-DD"))
		return
	}
	end, err := parseTime(values.Get("end"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid end time; use RFC3339 or YYYY-MM-DD"))
		return
	}
	p, err := s.registry.Provider(sourceID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	granules, err := p.Search(r.Context(), model.Query{
		CollectionID: collectionID,
		Start:        start,
		End:          end,
	})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, provider.ErrUnsupportedCollection) || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "window") || strings.Contains(err.Error(), "after start") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":     p.Source(),
		"collection": collectionID,
		"start":      start,
		"end":        end,
		"count":      len(granules),
		"granules":   granules,
	})
}

func parseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("missing time")
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	return time.ParseInLocation(time.DateOnly, value, time.UTC)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
