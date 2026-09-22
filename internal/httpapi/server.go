package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
	webui "github.com/nuist-fy-lab/satellite-data/web"
)

type Server struct {
	registry *provider.Registry
	store    *state.Store
}

func New(registry *provider.Registry, store *state.Store) http.Handler {
	s := &Server{registry: registry, store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/sources", s.sources)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/search", s.search)
	mux.HandleFunc("GET /api/products", s.products)
	mux.HandleFunc("GET /api/year", s.year)
	mux.HandleFunc("GET /api/day", s.day)
	mux.HandleFunc("POST /api/index", s.indexRange)

	static, err := fs.Sub(webui.Files, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(static))
	return withHeaders(mux)
}

func (s *Server) products(w http.ResponseWriter, r *http.Request) {
	sourceID := strings.TrimSpace(r.URL.Query().Get("source"))
	products, err := s.store.Products(r.Context(), sourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, products)
}

func (s *Server) year(w http.ResponseWriter, r *http.Request) {
	product := strings.TrimSpace(r.URL.Query().Get("product"))
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil || year < 1970 || year > 2100 {
		writeError(w, http.StatusBadRequest, errors.New("invalid year"))
		return
	}
	if _, _, err := s.store.ProductSource(r.Context(), product); err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	}
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	todayEnd := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	if end.After(todayEnd) {
		end = todayEnd
	}
	if start.Before(end) && r.URL.Query().Get("enqueue") == "1" {
		if err := s.store.EnqueueRange(r.Context(), product, start, end, 10); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	payload, err := s.store.Year(r.Context(), product, year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) day(w http.ResponseWriter, r *http.Request) {
	product := strings.TrimSpace(r.URL.Query().Get("product"))
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	at, err := time.Parse(time.DateOnly, day)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid day"))
		return
	}
	if _, _, err := s.store.ProductSource(r.Context(), product); err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	}
	if !at.After(time.Now().UTC()) {
		if err := s.store.EnqueueRange(r.Context(), product, at, at.AddDate(0, 0, 1), 100); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	payload, err := s.store.Day(r.Context(), product, day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) indexRange(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	product := strings.TrimSpace(values.Get("product"))
	start, err := time.Parse(time.DateOnly, strings.TrimSpace(values.Get("start")))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid start day"))
		return
	}
	end, err := time.Parse(time.DateOnly, strings.TrimSpace(values.Get("end")))
	if err != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, errors.New("invalid end day"))
		return
	}
	if _, _, err := s.store.ProductSource(r.Context(), product); err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	}
	todayEnd := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	if end.After(todayEnd) {
		end = todayEnd
	}
	if start.Before(end) {
		if err := s.store.EnqueueRange(r.Context(), product, start, end, 100); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "product": product, "start": start.Format(time.DateOnly), "end": end.Format(time.DateOnly)})
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
