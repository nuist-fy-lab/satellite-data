package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/central"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type cdsTransport func(*http.Request) (*http.Response, error)

func (f cdsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestERA5RequiresAdminAndNeverUsesTheDownloaderForSubmissions(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := authstore.Open(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	user, err := auth.CreateUser(t.Context(), "admin", "correct horse battery staple", "admin")
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.CreateSession(t.Context(), user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	client := &http.Client{Transport: cdsTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"id":"reanalysis-era5-single-levels","inputs":{"variable":{}}}`
		code := http.StatusOK
		if r.Method == http.MethodPost {
			body = `{"jobID":"request-1","status":"accepted"}`
			code = http.StatusAccepted
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := era5.New(client)
	vault, err := central.OpenVault(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	access, err := central.OpenStore(filepath.Join(dir, "access.db"), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	handler := NewWithAuth(provider.NewRegistry(p), store, nil, auth, time.Hour, central.NewService(access, client))
	body := `{"collection":"era5:reanalysis-era5-single-levels","inputs":{"variable":["2m_temperature"]}}`
	for _, endpoint := range []struct{ method, url string }{
		{http.MethodPost, "/api/v1/era5/requests"},
		{http.MethodGet, "/api/v1/era5/requests/request-1"},
		{http.MethodGet, "/api/v1/era5/requests/request-1/result"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(endpoint.method, endpoint.url, strings.NewReader(body)))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized status=%d", w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized access reached CDS")
	}
	request := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/era5/requests", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: accessSessionCookie, Value: session})
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request("https://other.example"))
	if w.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatal("cross-origin mutation accepted")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request(""))
	if w.Code != http.StatusServiceUnavailable || calls.Load() != 0 {
		t.Fatalf("unconfigured token: status=%d calls=%d", w.Code, calls.Load())
	}
	credential, err := access.UpsertCredential(t.Context(), central.CredentialSpec{Alias: "CDS", Kind: "cds", Secret: central.Secret{Token: "test-only-placeholder"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.UpsertAccount(t.Context(), central.AccountSpec{Alias: "ERA5", SourceID: "era5", CredentialID: &credential.ID, Enabled: true, ProxyMode: "direct"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request(""))
	if w.Code != http.StatusAccepted || w.Header().Get("Location") != "/api/v1/era5/requests/request-1" || calls.Load() != 2 {
		t.Fatalf("submission: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "test-only-placeholder") {
		t.Fatal("unsafe job response")
	}
}

func TestERA5CatalogueDaysDoNotQueueIndexWork(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "era5:reanalysis-era5-single-levels", SourceID: "era5", Metadata: map[string]string{"availability_mode": "on-demand"}}
	if err := store.UpsertProducts(t.Context(), "era5", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	handler := New(provider.NewRegistry(era5.New(nil)), store)
	for _, endpoint := range []struct{ method, url string }{
		{http.MethodGet, "/api/day?product=" + product.ProductKey + "&day=2025-01-01"},
		{http.MethodPost, "/api/index?product=" + product.ProductKey + "&start=2025-01-01&end=2025-01-02"},
		{http.MethodGet, "/api/v1/search?source=era5&collection=" + product.ProductKey + "&start=2025-01-01&end=2025-01-02"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(endpoint.method, endpoint.url, nil))
		if w.Code != http.StatusConflict {
			t.Fatalf("endpoint %s: status=%d body=%s", endpoint.url, w.Code, w.Body.String())
		}
	}
	batch, err := store.ClaimBatch(t.Context(), 7)
	if err != nil || len(batch.Days) != 0 {
		t.Fatalf("implicit CDS index work: %#v err=%v", batch, err)
	}
}
