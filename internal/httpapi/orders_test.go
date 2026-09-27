package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
			body, _ := json.Marshal(map[string]any{"product": product, "start": "2024-01-01", "end": "2024-01-02", "inputs": inputs})
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

func TestNonadminCannotQueueERA5ViaGenericDownloadRoute(t *testing.T) {
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
	const product = "era5:reanalysis-era5-single-levels"
	if err := s.UpsertProducts(t.Context(), "era5", []model.Product{{ProductKey: product, SourceID: "era5"}}); err != nil {
		t.Fatal(err)
	}
	registry, _ := app.NewRegistry()
	handler := NewWithAuth(registry, s, nil, auth, time.Hour)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/downloads", strings.NewReader(`{"product":"era5:reanalysis-era5-single-levels","start":"2024-01-01","end":"2024-01-02","inputs":{"variable":["2m_temperature"]}}`))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("nonadmin queued CDS work: %d %s", w.Code, w.Body.String())
	}
	orders, _ := s.DownloadRequests(t.Context(), 50)
	if len(orders) != 0 {
		t.Fatal("unauthorized request left a persisted order")
	}
}
