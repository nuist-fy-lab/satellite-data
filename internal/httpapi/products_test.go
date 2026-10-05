package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
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
