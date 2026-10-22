package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestOneProductOnlyOrderForEveryRegisteredSource(t *testing.T) {
	dir := t.TempDir()
	s, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth, err := authstore.Open(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	admin, err := auth.CreateUser(t.Context(), "operator", "fixture-test-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := auth.CreateAPIKey(t.Context(), admin.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, s, nil, auth, time.Hour)
	for _, source := range registry.Sources() {
		t.Run(source.ID, func(t *testing.T) {
			product := source.ID + ":fixture-product"
			inputs := map[string]any{}
			if source.ID == "era5" {
				product = "era5:reanalysis-era5-single-levels"
				inputs["variable"] = []string{"2m_temperature"}
			}
			if err := s.UpsertProducts(t.Context(), source.ID, []model.Product{{ProductKey: product, SourceID: source.ID}}); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"product": product, "start": "2024-01-01", "end": "2024-01-02", "scenes": nil, "inputs": inputs})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/downloads", strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusAccepted {
				t.Fatalf("product-only order: %d %s", w.Code, w.Body.String())
			}
			var response struct {
				Download state.DownloadRequest `json:"download"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Download.SourceID != source.ID || response.Download.CollectionID != product || response.Download.ID <= 0 || response.Download.State != "pending" {
				t.Fatalf("invalid centre order: %+v", response.Download)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/catalog", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var catalog struct {
		Products []struct {
			model.Product
			OrderMode string `json:"order_mode"`
		} `json:"products"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Products) != len(registry.Sources()) {
		t.Fatalf("all-source catalogue: %d %s", w.Code, w.Body.String())
	}
	for _, item := range catalog.Products {
		if item.SourceID == "era5" && item.OrderMode != "on-demand" {
			t.Fatal("on-demand catalogue incorrectly advertises a granule archive")
		}
	}
	orders, err := s.DownloadRequests(t.Context(), 50)
	if err != nil || len(orders) != len(registry.Sources()) {
		t.Fatalf("wrong persisted order count: %d %v", len(orders), err)
	}
}

func TestOwnedERA5OrdersAndLegacyVisibility(t *testing.T) {
	dir := t.TempDir()
	s, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth, err := authstore.Open(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	reader, err := auth.CreateUser(t.Context(), "reader", "fixture-test-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := auth.CreateAPIKey(t.Context(), reader.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.CreateUser(t.Context(), "other", "fixture-test-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, err := auth.CreateAPIKey(t.Context(), other.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := auth.CreateUser(t.Context(), "operator", "fixture-test-password", "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, adminKey, err := auth.CreateAPIKey(t.Context(), admin.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	const product = "era5:reanalysis-era5-single-levels"
	if err := s.UpsertProducts(t.Context(), "era5", []model.Product{{ProductKey: product, SourceID: "era5"}}); err != nil {
		t.Fatal(err)
	}
	registry, _ := app.NewRegistry()
	handler := NewWithAuth(registry, s, nil, auth, time.Hour)
	legacy, err := s.EnqueueERA5Order(t.Context(), product, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)})
	if err != nil {
		t.Fatal(err)
	}
	call := func(key, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	foreign := httptest.NewRequest(http.MethodPost, "/api/v1/downloads", strings.NewReader(`{"product":"era5:reanalysis-era5-single-levels","start":"2024-01-01","end":"2024-01-02","inputs":{"variable":["2m_temperature"]}}`))
	foreign.Header.Set("Authorization", "Bearer "+key)
	foreign.Header.Set("Origin", "https://untrusted.example")
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, foreign)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("cross-origin ERA5 order accepted: %d", blocked.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/downloads", strings.NewReader(`{"product":"era5:reanalysis-era5-single-levels","start":"2024-01-01","end":"2024-01-02","inputs":{"variable":["2m_temperature"]}}`))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ordinary user could not queue ERA5: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Download state.DownloadRequest `json:"download"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Download.ID <= legacy.ID || strings.Contains(w.Body.String(), reader.ID) {
		t.Fatalf("invalid private order response: %s err=%v", w.Body.String(), err)
	}
	if item, err := s.DownloadRequest(t.Context(), created.Download.ID); err != nil || item.OwnerUserID != reader.ID {
		t.Fatalf("order ownership not persisted: %+v err=%v", item, err)
	}
	for _, tc := range []struct {
		key             string
		wantList, owned bool
	}{
		{key: key, wantList: true, owned: true},
		{key: otherKey, wantList: false, owned: false},
		{key: adminKey, wantList: true, owned: true},
	} {
		response := call(tc.key, http.MethodGet, "/api/v1/downloads", "")
		var listing struct {
			Downloads []state.DownloadRequest `json:"downloads"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &listing) != nil {
			t.Fatalf("order list: %d %s", response.Code, response.Body.String())
		}
		if got := len(listing.Downloads); (got > 0) != tc.wantList || (tc.key == adminKey && got != 2) {
			t.Fatalf("wrong visible orders: %d for key role", got)
		}
		status := call(tc.key, http.MethodGet, "/api/v1/downloads/"+strconv.FormatInt(created.Download.ID, 10), "")
		if status.Code != map[bool]int{true: http.StatusOK, false: http.StatusNotFound}[tc.owned] {
			t.Fatalf("owned order visibility: %d", status.Code)
		}
	}
	if status := call(key, http.MethodGet, "/api/v1/downloads/"+strconv.FormatInt(legacy.ID, 10), ""); status.Code != http.StatusNotFound {
		t.Fatalf("legacy unowned order leaked: %d", status.Code)
	}
	if status := call(key, http.MethodPost, "/api/v1/era5/requests", `{"collection":"era5:reanalysis-era5-single-levels","inputs":{"variable":["2m_temperature"]}}`); status.Code != http.StatusForbidden {
		t.Fatalf("low-level CDS operation is no longer admin-only: %d", status.Code)
	}
}
