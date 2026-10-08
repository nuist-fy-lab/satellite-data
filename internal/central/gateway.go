package central

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var sources = []string{"goes", "himawari8", "himawari9", "modis", "viirs", "mtg", "earthcare", "calipso", "cloudsat", "era5"}

func allowedHost(source, host string) bool {
	switch source {
	case "goes":
		return host == "storage.googleapis.com"
	case "himawari8":
		return host == "noaa-himawari8.s3.amazonaws.com"
	case "himawari9":
		return host == "noaa-himawari9.s3.amazonaws.com"
	case "modis", "viirs":
		return host == "ladsweb.modaps.eosdis.nasa.gov" || host == "data.laadsdaac.earthdatacloud.nasa.gov"
	case "mtg":
		return host == "api.eumetsat.int"
	case "earthcare":
		return host == "catalog.maap.eo.esa.int"
	case "calipso":
		return host == "data.asdc.earthdata.nasa.gov"
	case "cloudsat":
		return host == "dpc-thredds.cira.colostate.edu"
	case "era5":
		return host == "cds.climate.copernicus.eu"
	default:
		return false
	}
}

var cdsDownloadPath = regexp.MustCompile(`^/api/retrieve/v1/jobs/([A-Za-z0-9][A-Za-z0-9_-]{0,127})/download$`)

func cdsAssetHost(host string) bool {
	return host == "object-store.os-api.cci1.ecmwf.int" || host == "object-store.os-api.cci2.ecmwf.int"
}

func (s *Service) Gateway() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/access/status", func(w http.ResponseWriter, r *http.Request) {
		status := make(map[string]bool, len(sources))
		for _, source := range sources {
			route, err := s.resolve(r.Context(), source, nil)
			status[source] = err == nil
			if err == nil && !route.keepIdleConnections {
				route.client.CloseIdleConnections()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": status})
	})
	mux.HandleFunc("POST /v1/fetch", s.fetch)
	return mux
}

func (s *Service) fetch(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var request struct {
		SourceID string `json:"source_id"`
		URL      string `json:"url"`
		Range    string `json:"range,omitempty"`
		IfRange  string `json:"if_range,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		http.Error(w, "invalid centre fetch request", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(request.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || !allowedHost(request.SourceID, u.Hostname()) {
		http.Error(w, "asset URL is not allowed for provider", http.StatusBadRequest)
		return
	}
	var binding *Binding
	var cdsJobID string
	if request.SourceID == "era5" {
		match := cdsDownloadPath.FindStringSubmatch(u.Path)
		if len(match) != 2 || u.RawQuery != "" || u.Fragment != "" {
			http.Error(w, "invalid CDS job download reference", http.StatusBadRequest)
			return
		}
		cdsJobID = match[1]
		original, err := s.Store.Binding(r.Context(), cdsJobID)
		if err != nil {
			http.Error(w, "CDS centre job route not found", http.StatusNotFound)
			return
		}
		binding = &original
	}
	route, err := s.resolve(r.Context(), request.SourceID, binding)
	if err != nil {
		http.Error(w, "centre access route is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !route.keepIdleConnections {
		defer route.client.CloseIdleConnections()
	}
	s.Store.markUsed(r.Context(), route)
	if cdsJobID != "" {
		route.client.Timeout = 60 * time.Second
		asset, err := route.provider.Result(r.Context(), cdsJobID)
		route.client.Timeout = 0
		if err != nil {
			http.Error(w, "CDS result is unavailable", http.StatusBadGateway)
			return
		}
		u, err = url.Parse(asset.URL)
		if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || !cdsAssetHost(u.Hostname()) {
			http.Error(w, "CDS result host is not allowed", http.StatusBadGateway)
			return
		}
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, "invalid asset request", http.StatusBadRequest)
		return
	}
	diagnostics := newTransferDiagnostics(request.SourceID, u.Hostname(), path.Base(u.Path), request.Range)
	upstream = upstream.WithContext(httptrace.WithClientTrace(upstream.Context(), diagnostics.trace()))
	stopDiagnostics := diagnostics.start(upstream.Context())
	defer stopDiagnostics()
	upstream.Header.Set("Accept-Encoding", "identity")
	if request.Range != "" {
		upstream.Header.Set("Range", request.Range)
	}
	if request.IfRange != "" {
		upstream.Header.Set("If-Range", request.IfRange)
	}
	if route.authorizer != nil && cdsJobID == "" {
		if err := route.authorizer.Authorize(r.Context(), upstream); err != nil {
			s.Store.markResult(context.WithoutCancel(r.Context()), route, false)
			http.Error(w, "centre provider credential was rejected", http.StatusUnauthorized)
			return
		}
	}
	route.client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many asset redirects")
		}
		if next.URL.Scheme != "https" || next.URL.User != nil || (next.URL.Port() != "" && next.URL.Port() != "443") {
			return errors.New("unsafe asset redirect")
		}
		host := next.URL.Hostname()
		if cdsJobID != "" && !cdsAssetHost(host) {
			return errors.New("CDS asset redirect host is not allowed")
		}
		if cdsJobID == "" && !allowedHost(request.SourceID, host) && !strings.HasSuffix(host, ".s3.amazonaws.com") && !strings.HasSuffix(host, ".amazonaws.com") && host != "storage.googleapis.com" {
			return errors.New("asset redirect host is not allowed")
		}
		if host != u.Hostname() {
			next.Header.Del("Authorization")
			next.Header.Del("PRIVATE-TOKEN")
			next.Header.Del("Cookie")
		}
		return nil
	}
	response, err := route.client.Do(upstream)
	if err != nil {
		s.Store.markProxyFailure(r.Context(), route)
		http.Error(w, "centre upstream request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		s.Store.markResult(context.WithoutCancel(r.Context()), route, false)
		http.Error(w, "centre provider credential was rejected", response.StatusCode)
		return
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Retry-After"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, copyErr := io.CopyBuffer(diagnostics.writer(w), response.Body, make([]byte, 128<<10))
	if copyErr == nil && response.StatusCode/100 == 2 {
		s.Store.markResult(context.WithoutCancel(r.Context()), route, true)
	}
}

func (s *Store) markResult(ctx context.Context, route route, success bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, detail := "ready", ""
	if !success {
		state, detail = "auth_required", "provider credential was rejected"
	}
	for _, item := range []struct {
		table   string
		id      int64
		version string
	}{{"accounts", route.accountID, route.accountVersion}, {"credentials", route.credentialID, route.credentialVersion}} {
		if item.id <= 0 {
			continue
		}
		_, _ = s.db.ExecContext(ctx, "UPDATE "+item.table+" SET metadata=json_set(metadata,'$.state',?,'$.last_error',?,'$.last_success_at',CASE WHEN ? THEN ? ELSE COALESCE(json_extract(metadata,'$.last_success_at'),'') END) WHERE id=? AND json_extract(metadata,'$.updated_at')=?", state, detail, success, stamp(), item.id, item.version)
	}
	if success && route.proxyID > 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE proxies SET metadata=json_set(metadata,'$.state','ready','$.last_error','','$.last_success_at',?) WHERE id=? AND json_extract(metadata,'$.updated_at')=?`, stamp(), route.proxyID, route.proxyVersion)
	}
}
func (s *Store) markUsed(ctx context.Context, route route) {
	for _, item := range []struct {
		table string
		id    int64
	}{{"accounts", route.accountID}, {"credentials", route.credentialID}, {"proxies", route.proxyID}} {
		if item.id > 0 {
			_, _ = s.db.ExecContext(ctx, "UPDATE "+item.table+" SET metadata=json_set(metadata,'$.last_used_at',?) WHERE id=?", stamp(), item.id)
		}
	}
}
func (s *Store) markProxyFailure(ctx context.Context, route route) {
	if route.proxyID > 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE proxies SET metadata=json_set(metadata,'$.state','degraded','$.last_error','upstream network request failed') WHERE id=? AND json_extract(metadata,'$.updated_at')=?`, route.proxyID, route.proxyVersion)
	}
}

func RunGateway(ctx context.Context, filename string, service *Service) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(filename); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("centre IPC path is not a socket")
		}
		conn, err := net.DialTimeout("unix", filename, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return errors.New("another centre IPC process is active")
		}
		if err := os.Remove(filename); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", filename)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(filename)
	if err := os.Chmod(filename, 0600); err != nil {
		return err
	}
	server := &http.Server{Handler: service.Gateway(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
