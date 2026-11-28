package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestProductsSupportsMultipleSources(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	for _, product := range []model.Product{
		{ProductKey: "goes16:a", SourceID: "goes16", Satellite: "GOES-16", DataType: "A"},
		{ProductKey: "goes18:b", SourceID: "goes18", Satellite: "GOES-18", DataType: "B"},
		{ProductKey: "modis:c", SourceID: "modis", Satellite: "Terra", DataType: "C"},
	} {
		if err := store.UpsertProducts(ctx, product.SourceID, []model.Product{product}); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(nil, store)
	req := httptest.NewRequest(http.MethodGet, "/api/products?sources=goes16,goes18", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var products []model.Product
	if err := json.Unmarshal(rec.Body.Bytes(), &products); err != nil {
		t.Fatal(err)
	}
	if len(products) != 2 || products[0].SourceID != "goes16" || products[1].SourceID != "goes18" {
		t.Fatalf("products=%+v", products)
	}
}

func TestCalendarCatalogBootstrapsSourcesAndOneFamily(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := provider.NewRegistry(&queryProvider{}, &availableTestProvider{source: "modis"})
	for _, source := range []string{"goes18", "modis"} {
		if err := store.UpsertProducts(t.Context(), source, []model.Product{{ProductKey: source + ":a", SourceID: source}}); err != nil {
			t.Fatal(err)
		}
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/products?bootstrap=1&stream=1&family=goes", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	New(registry, store).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/x-ndjson") || recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("catalog stream response: %d %v", recorder.Code, recorder.Header())
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	var header struct {
		Sources []model.Source `json:"sources"`
		Family  string         `json:"family"`
	}
	if err := decoder.Decode(&header); err != nil || header.Family != "goes" || len(header.Sources) != 2 {
		t.Fatalf("catalog stream header: %+v, %v", header, err)
	}
	var product model.Product
	if err := decoder.Decode(&product); err != nil || product.ProductKey != "goes18:a" {
		t.Fatalf("catalog stream product: %+v, %v", product, err)
	}
	var end struct{ Done bool }
	if err := decoder.Decode(&end); err != nil || !end.Done {
		t.Fatalf("catalog stream completion: %+v, %v", end, err)
	}
}
