package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadcoord"
	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/linkmon"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
	webui "github.com/nuist-fy-lab/satellite-data/web"
)

const accessSessionCookie = "satellite_access_session"

type Server struct {
	registry   *provider.Registry
	store      *state.Store
	downloader *downloader.Client
	auth       *authstore.Store
	sessionTTL time.Duration
	central    *central.Service
	portalAuth bool
}

func New(registry *provider.Registry, store *state.Store, clients ...*downloader.Client) http.Handler {
	var downloaderClient *downloader.Client
	if len(clients) > 0 {
		downloaderClient = clients[0]
	}
	return newHandler(registry, store, downloaderClient, nil, 0, false)
}

func NewWithAuth(registry *provider.Registry, store *state.Store, downloaderClient *downloader.Client, auth *authstore.Store, sessionTTL time.Duration, services ...*central.Service) http.Handler {
	return newHandler(registry, store, downloaderClient, auth, sessionTTL, true, services...)
}

func newHandler(registry *provider.Registry, store *state.Store, downloaderClient *downloader.Client, auth *authstore.Store, sessionTTL time.Duration, portalAuth bool, services ...*central.Service) http.Handler {
	s := &Server{registry: registry, store: store, downloader: downloaderClient, auth: auth, sessionTTL: sessionTTL, portalAuth: portalAuth}
	if len(services) > 0 {
		s.central = services[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /login", s.accessLogin)
	mux.HandleFunc("POST /logout", s.accessLogout)
	mux.HandleFunc("POST /api/v1/auth/login", s.accessLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.accessLogout)
	mux.HandleFunc("GET /api/v1/auth/session", s.portalSession)
	mux.HandleFunc("GET /api/v1/sources", s.sources)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/search", s.search)
	mux.HandleFunc("GET /api/v1/era5/schema", s.era5Schema)
	mux.HandleFunc("POST /api/v1/era5/requests", s.era5Submit)
	mux.HandleFunc("GET /api/v1/era5/requests/{id}", s.era5Job)
	mux.HandleFunc("GET /api/v1/era5/requests/{id}/result", s.era5Result)
	mux.HandleFunc("POST /api/v1/downloads", s.createDownload)
	mux.HandleFunc("GET /api/v1/downloads", s.downloads)
	mux.HandleFunc("GET /api/v1/downloads/updates", s.downloadUpdates)
	mux.HandleFunc("GET /api/v1/downloads/catalog", s.downloadCatalog)
	mux.HandleFunc("GET /api/v1/downloads/{id}", s.download)
	mux.HandleFunc("GET /api/v1/downloader/providers", s.downloaderProviders)
	mux.HandleFunc("GET /api/v1/downloader/providers/stream", s.downloaderProviderStream)
	mux.HandleFunc("GET /api/v1/index/stream", s.indexStream)
	mux.HandleFunc("GET /api/v1/archive/index-status", s.archiveIndexStatus)
	mux.HandleFunc("GET /api/v1/monitor/downloads", s.downloadMonitor)
	mux.HandleFunc("GET /api/v1/monitor/links", s.linkMonitor)
	mux.HandleFunc("GET /api/v1/me/api-keys", s.listAPIKeys)
	mux.HandleFunc("POST /api/v1/me/api-keys", s.createAPIKey)
	mux.HandleFunc("DELETE /api/v1/me/api-keys/{id}", s.revokeAPIKey)
	mux.HandleFunc("POST /api/v1/access/login", s.accessLogin)
	mux.HandleFunc("POST /api/v1/access/logout", s.accessLogout)
	mux.HandleFunc("GET /api/v1/access/session", s.accessSession)
	mux.HandleFunc("GET /api/v1/access/pools", s.accessPools)
	mux.HandleFunc("POST /api/v1/access/credentials", s.accessCredential)
	mux.HandleFunc("DELETE /api/v1/access/credentials/{id}", s.deleteAccessCredential)
	mux.HandleFunc("POST /api/v1/access/proxies", s.accessProxy)
	mux.HandleFunc("DELETE /api/v1/access/proxies/{id}", s.deleteAccessProxy)
	mux.HandleFunc("POST /api/v1/access/accounts", s.accessAccount)
	mux.HandleFunc("DELETE /api/v1/access/accounts/{id}", s.deleteAccessAccount)
	mux.HandleFunc("POST /api/v1/access/profiles", s.centralProfile)
	mux.HandleFunc("POST /api/v1/access/central/credentials", s.centralCredential)
	mux.HandleFunc("POST /api/v1/access/central/proxies", s.centralProxy)
	mux.HandleFunc("POST /api/v1/access/central/accounts", s.centralAccount)
	mux.HandleFunc("DELETE /api/v1/access/central/{kind}/{id}", s.centralDelete)
	mux.HandleFunc("GET /api/products", s.products)
	mux.HandleFunc("GET /api/year", s.year)
	mux.HandleFunc("GET /api/day", s.day)
	mux.HandleFunc("GET /api/coverage/year", s.coverageYear)
	mux.HandleFunc("GET /api/coverage/month", s.coverageMonth)
	mux.HandleFunc("GET /api/coverage/day", s.coverageDay)
	mux.HandleFunc("GET /api/coverage/files", s.coverageFiles)
	mux.HandleFunc("POST /api/index", s.indexRange)

	static, err := fs.Sub(webui.Files, "static")
	if err != nil {
		panic(err)
	}
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.webUser(r); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		data, err := fs.ReadFile(static, "login.html")
		if err != nil {
			http.Error(w, "login page unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	servePortal := func(w http.ResponseWriter, _ *http.Request) {
		data, err := fs.ReadFile(static, "index.html")
		if err != nil {
			http.Error(w, "portal unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}
	for _, path := range []string{"/calendar", "/sources", "/downloads", "/sync", "/monitor", "/cli", "/access"} {
		mux.HandleFunc("GET "+path, servePortal)
	}
	mux.Handle("GET /", http.FileServerFS(static))
	return withCompression(withHeaders(s.withPortalAuth(mux)))
}

func (s *Server) archiveIndexStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.ArchiveScanStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

type monitorWindow struct {
	duration time.Duration
	bucket   time.Duration
}

var monitorWindows = map[string]monitorWindow{
	"1h":  {duration: time.Hour, bucket: 15 * time.Second},
	"3h":  {duration: 3 * time.Hour, bucket: 30 * time.Second},
	"6h":  {duration: 6 * time.Hour, bucket: time.Minute},
	"12h": {duration: 12 * time.Hour, bucket: 2 * time.Minute},
	"1d":  {duration: 24 * time.Hour, bucket: 5 * time.Minute},
	"2d":  {duration: 48 * time.Hour, bucket: 10 * time.Minute},
	"1w":  {duration: 7 * 24 * time.Hour, bucket: 30 * time.Minute},
}

func (s *Server) downloadMonitor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("window"))
	if name == "" {
		name = "1h"
	}
	window, ok := monitorWindows[name]
	if name == "realtime" {
		window, ok = monitorWindow{duration: 5 * time.Minute, bucket: 15 * time.Second}, true
	}
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("unsupported download monitor window"))
		return
	}
	seriesSince := time.Now().UTC().Add(-window.duration)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid monitor cursor"))
			return
		}
		if at.After(seriesSince) {
			seriesSince = at.UTC()
		}
	}
	latest, err := s.store.LatestDownloadSamples(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	points, err := s.store.DownloadSeries(r.Context(), seriesSince, window.bucket)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type entry struct {
		SourceID string                       `json:"source_id"`
		Latest   *state.DownloadMonitorLatest `json:"latest,omitempty"`
		Points   []state.DownloadMonitorPoint `json:"points"`
	}
	bySource := make(map[string]*entry)
	for _, item := range latest {
		sample := item
		bySource[item.SourceID] = &entry{SourceID: item.SourceID, Latest: &sample, Points: []state.DownloadMonitorPoint{}}
	}
	for source, series := range points {
		if bySource[source] == nil {
			bySource[source] = &entry{SourceID: source}
		}
		bySource[source].Points = series
	}
	keys := make([]string, 0, len(bySource))
	for key := range bySource {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]entry, 0, len(keys))
	for _, key := range keys {
		items = append(items, *bySource[key])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window": name, "bucket_seconds": int(window.bucket.Seconds()),
		"sample_interval_seconds": 15, "generated_at": time.Now().UTC(), "providers": items,
	})
}

func (s *Server) linkMonitor(w http.ResponseWriter, r *http.Request) {
	windowName := strings.TrimSpace(r.URL.Query().Get("window"))
	if windowName == "" {
		windowName = "1h"
	}
	window, ok := monitorWindows[windowName]
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("unsupported monitor window"))
		return
	}
	seriesSince := time.Now().UTC().Add(-window.duration)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid monitor cursor"))
			return
		}
		if at.After(seriesSince) {
			seriesSince = at.UTC()
		}
	}
	latest, err := s.store.LatestLinkSamples(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	series, err := s.store.LinkSeries(r.Context(), seriesSince, window.bucket)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	latestBySource := make(map[string]state.LinkSample, len(latest))
	for _, item := range latest {
		latestBySource[item.SourceID] = item
	}
	type link struct {
		SourceID string            `json:"source_id"`
		Name     string            `json:"name"`
		Target   string            `json:"target"`
		Port     int               `json:"port"`
		Latest   *state.LinkSample `json:"latest,omitempty"`
		Points   []state.LinkPoint `json:"points"`
	}
	items := make([]link, 0, len(linkmon.Targets()))
	for _, target := range linkmon.Targets() {
		item := link{
			SourceID: target.SourceID,
			Name:     target.Name,
			Target:   target.Host,
			Port:     target.Port,
			Points:   series[target.SourceID],
		}
		if current, exists := latestBySource[target.SourceID]; exists {
			value := current
			item.Latest = &value
		}
		if item.Points == nil {
			item.Points = []state.LinkPoint{}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"origin":                  "c2ln1",
		"window":                  windowName,
		"bucket_seconds":          int(window.bucket.Seconds()),
		"sample_interval_seconds": 15,
		"generated_at":            time.Now().UTC(),
		"links":                   items,
	})
}

func (s *Server) accessLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("access management authentication is not configured"))
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	isForm := strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	if isForm {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid login form"))
			return
		}
		request.Username, request.Password = r.PostFormValue("username"), r.PostFormValue("password")
	} else {
		if err := decodeRequestJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	user, err := s.auth.VerifyPassword(r.Context(), request.Username, request.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid portal credentials"))
		return
	}
	ttl := s.sessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	token, err := s.auth.CreateSession(r.Context(), user.ID, ttl)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session unavailable"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: accessSessionCookie, Value: token, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	if isForm {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) accessLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		if cookie, err := r.Cookie(accessSessionCookie); err == nil {
			_ = s.auth.RevokeSession(r.Context(), cookie.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: accessSessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) accessSession(w http.ResponseWriter, r *http.Request) {
	user, err := s.adminUser(r)
	if err != nil {
		s.writeAdminError(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) accessPools(w http.ResponseWriter, r *http.Request) {
	if _, err := s.adminUser(r); err != nil {
		s.writeAdminError(w, r)
		return
	}
	if s.central == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	pools, err := s.central.Store.Pools(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("central access store unavailable"))
		return
	}
	writeJSON(w, http.StatusOK, pools)
}

func (s *Server) accessCredential(w http.ResponseWriter, r *http.Request) {
	s.centralCredential(w, r)
}

func (s *Server) deleteAccessCredential(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("kind", "credentials")
	s.centralDelete(w, r)
}

func (s *Server) accessProxy(w http.ResponseWriter, r *http.Request) {
	s.centralProxy(w, r)
}

func (s *Server) deleteAccessProxy(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("kind", "proxies")
	s.centralDelete(w, r)
}

func (s *Server) accessAccount(w http.ResponseWriter, r *http.Request) {
	s.centralAccount(w, r)
}

func (s *Server) deleteAccessAccount(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("kind", "accounts")
	s.centralDelete(w, r)
}

func (s *Server) authorizeAdminMutation(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.adminUser(r); err != nil {
		s.writeAdminError(w, r)
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			writeError(w, http.StatusForbidden, errors.New("cross-origin access mutation rejected"))
			return false
		}
	}
	return true
}

func (s *Server) writeAdminError(w http.ResponseWriter, r *http.Request) {
	if _, err := s.webUser(r); err == nil {
		writeError(w, http.StatusForbidden, errors.New("administrator role required"))
		return
	}
	writeError(w, http.StatusUnauthorized, errors.New("portal session required"))
}

func (s *Server) adminUser(r *http.Request) (authstore.User, error) {
	user, err := s.webUser(r)
	if err != nil || user.Role != "admin" {
		return authstore.User{}, errors.New("admin session required")
	}
	return user, nil
}

func (s *Server) webUser(r *http.Request) (authstore.User, error) {
	if s.auth == nil {
		return authstore.User{}, errors.New("auth unavailable")
	}
	cookie, err := r.Cookie(accessSessionCookie)
	if err != nil {
		return authstore.User{}, err
	}
	return s.auth.VerifySession(r.Context(), cookie.Value)
}

func decodeRequestJSON(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request JSON")
	}
	return nil
}

func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var payload struct {
		Source     string                     `json:"source"`
		Collection string                     `json:"collection"`
		Product    string                     `json:"product"`
		Start      string                     `json:"start"`
		End        string                     `json:"end"`
		Band       int                        `json:"band"`
		Times      []string                   `json:"times"`
		Inputs     map[string]json.RawMessage `json:"inputs"`
	}
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid download request JSON"))
		return
	}
	payload.Source = strings.TrimSpace(payload.Source)
	payload.Collection = strings.TrimSpace(payload.Collection)
	if payload.Product != "" {
		if payload.Collection != "" && payload.Collection != payload.Product {
			writeError(w, http.StatusBadRequest, errors.New("conflicting product and collection"))
			return
		}
		payload.Collection = strings.TrimSpace(payload.Product)
	}
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
	if payload.Source == "" {
		payload.Source = productSource
	}
	if productSource != payload.Source {
		writeError(w, http.StatusBadRequest, errors.New("collection does not belong to source"))
		return
	}
	if payload.Source == "era5" {
		if payload.Band != 0 {
			writeError(w, http.StatusBadRequest, errors.New("ERA5 uses variables, not --band"))
			return
		}
		if payload.Inputs == nil {
			payload.Inputs = make(map[string]json.RawMessage)
		}
		if len(payload.Times) > 0 {
			if _, exists := payload.Inputs["time"]; exists {
				writeError(w, http.StatusBadRequest, errors.New("time must be specified once"))
				return
			}
			selection, err := (downloadfilter.Selection{Times: payload.Times}).Normalize(payload.Source, payload.Collection)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			payload.Inputs["time"], _ = json.Marshal(selection.Times)
		}
		request, err := s.store.EnqueueERA5OrderOwned(r.Context(), user.ID, payload.Collection, start, end, payload.Inputs)
		if err != nil {
			writeERA5Error(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"download": request})
		return
	}
	if len(payload.Inputs) != 0 {
		writeError(w, http.StatusBadRequest, errors.New("inputs are only supported by on-demand products"))
		return
	}
	providerID, ok := downloadcoord.ProviderForSource(payload.Source)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("source does not have a downloader pipeline"))
		return
	}
	selection, err := (downloadfilter.Selection{Band: payload.Band, Times: payload.Times}).Normalize(payload.Source, payload.Collection)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request, err := s.store.EnqueueSelectedDownloadRequestOwned(r.Context(), user.ID, payload.Source, providerID, payload.Collection, start, end, selection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Keep the official calendar/index pipeline moving in parallel with the
	// download discovery pipeline. This is deliberately not awaited: download
	// discovery has its own provider-isolated workers and must not depend on the
	// calendar indexer finishing first.
	_ = s.store.EnqueueRange(r.Context(), payload.Collection, start, end, 200)
	writeJSON(w, http.StatusAccepted, map[string]any{"download": request})
}

func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var items []state.DownloadRequest
	var err error
	if user.Role == "admin" {
		items, err = s.store.DownloadRequests(r.Context(), limit)
	} else {
		items, err = s.store.DownloadRequestsOwned(r.Context(), limit, user.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloads": items})
}

// Catalog keeps selection metadata in the control plane; the downloader sees
// only concrete assets and never needs to know about satellites or bands.
func (s *Server) downloadCatalog(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source != "" {
		if _, err := s.registry.Provider(source); err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
	}
	products, err := s.store.Products(r.Context(), source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type option struct {
		model.Product
		Bands     []downloadfilter.Band `json:"bands"`
		OrderMode string                `json:"order_mode"`
	}
	items := make([]option, 0, len(products))
	for _, product := range products {
		item := option{Product: product, Bands: []downloadfilter.Band{}, OrderMode: "archive"}
		if product.SourceID == "era5" {
			item.OrderMode = "on-demand"
		}
		if profile := downloadfilter.ForProduct(product.SourceID, product.ProductKey); profile != nil {
			item.Bands = profile.Bands
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": items})
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	user, ok := s.orderUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid download id"))
		return
	}
	var item state.DownloadRequest
	if user.Role == "admin" {
		item, err = s.store.DownloadRequest(r.Context(), id)
	} else {
		item, err = s.store.DownloadRequestOwned(r.Context(), id, user.ID)
	}
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
	for i := range items {
		items[i].AuthRef = ""
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
	if _, selected, err := s.store.ProductSource(r.Context(), product); err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	} else if selected.Metadata["availability_mode"] == "on-demand" {
		writeError(w, http.StatusConflict, provider.ErrOnDemand)
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
	if _, selected, err := s.store.ProductSource(r.Context(), product); err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	} else if selected.Metadata["availability_mode"] == "on-demand" {
		writeError(w, http.StatusConflict, provider.ErrOnDemand)
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
	if key := strings.TrimSpace(values.Get("product")); key != "" {
		if collectionID != "" && collectionID != key {
			writeError(w, http.StatusBadRequest, errors.New("conflicting product and collection"))
			return
		}
		collectionID = key
	}
	if prefix, suffix, ok := strings.Cut(collectionID, ":"); ok {
		if sourceID != "" && sourceID != prefix {
			writeError(w, http.StatusBadRequest, errors.New("collection does not belong to source"))
			return
		}
		if suffix == "" {
			writeError(w, http.StatusBadRequest, errors.New("collection is required"))
			return
		}
		sourceID = prefix
	} else if sourceID != "" && collectionID != "" {
		collectionID = sourceID + ":" + collectionID
	}
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
	band := 0
	if value := values.Get("band"); value != "" {
		band, err = strconv.Atoi(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid band"))
			return
		}
	}
	selection, err := (downloadfilter.Selection{Band: band}).Normalize(sourceID, collectionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	granules, err := searchGranules(r.Context(), p, model.Query{
		CollectionID: collectionID,
		Start:        start,
		End:          end,
	}, selection)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, provider.ErrOnDemand) {
			status = http.StatusConflict
		} else if errors.Is(err, provider.ErrUnsupportedCollection) || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "window") || strings.Contains(err.Error(), "after start") {
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
		if isVersionedStaticAsset(r) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func isVersionedStaticAsset(r *http.Request) bool {
	if strings.TrimSpace(r.URL.Query().Get("v")) == "" {
		return false
	}
	path := strings.ToLower(r.URL.Path)
	return strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") || strings.HasSuffix(path, ".svg") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".webp")
}
