package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadcoord"
	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
	webui "github.com/nuist-fy-lab/satellite-data/web"
)

type Server struct {
	registry   *provider.Registry
	store      *state.Store
	downloader *downloader.Client
}

func New(registry *provider.Registry, store *state.Store, clients ...*downloader.Client) http.Handler {
	var downloaderClient *downloader.Client
	if len(clients) > 0 {
		downloaderClient = clients[0]
	}
	s := &Server{registry: registry, store: store, downloader: downloaderClient}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/sources", s.sources)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/search", s.search)
	mux.HandleFunc("POST /api/v1/downloads", s.createDownload)
	mux.HandleFunc("GET /api/v1/downloads", s.downloads)
	mux.HandleFunc("GET /api/v1/downloads/{id}", s.download)
	mux.HandleFunc("GET /api/v1/downloader/providers", s.downloaderProviders)
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

func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var payload struct {
		Source     string `json:"source"`
		Collection string `json:"collection"`
		Start      string `json:"start"`
		End        string `json:"end"`
	}
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid download request JSON"))
		return
	}
	payload.Source = strings.TrimSpace(payload.Source)
	payload.Collection = strings.TrimSpace(payload.Collection)
	start, err := parseTime(payload.Start)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid start time; use RFC3339 or YYYY-MM-DD"))
		return
	}
	end, err := parseTime(payload.End)
	if err != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, errors.New("invalid end time; it must be after start"))
		return
	}
	productSource, _, err := s.store.ProductSource(r.Context(), payload.Collection)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown collection"))
		return
	}
	if productSource != payload.Source {
		writeError(w, http.StatusBadRequest, errors.New("collection does not belong to source"))
		return
	}
	providerID, ok := downloadcoord.ProviderForSource(payload.Source)
	if !ok {
		if payload.Source == "himawari8" || payload.Source == "himawari9" {
			writeError(w, http.StatusConflict, errors.New("Himawari-8/9 downloads are temporarily paused on c2ln1"))
			return
		}
		writeError(w, http.StatusBadRequest, errors.New("source does not have a downloader pipeline"))
		return
	}
	request, err := s.store.EnqueueDownloadRequest(r.Context(), payload.Source, providerID, payload.Collection, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Keep the official calendar/index pipeline moving in parallel with the
	// download discovery pipeline. This is deliberately not awaited: download
	// discovery has its own provider-isolated workers and must not depend on the
	// calendar indexer finishing first.
	if err := s.store.EnqueueRange(r.Context(), payload.Collection, start, end, 200); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"download": request})
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.DownloadRequests(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloads": items})
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid download id"))
		return
	}
	item, err := s.store.DownloadRequest(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("download request not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"download": item})
}

func (s *Server) downloaderProviders(w http.ResponseWriter, r *http.Request) {
	if s.downloader == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("satellite downloader is not configured"))
		return
	}
	items, err := s.downloader.Providers(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
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
