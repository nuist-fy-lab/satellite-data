package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestCatalogueVersionAlternativesDoNotMergeSourcesOrSatellites(t *testing.T) {
	products := []model.Product{
		{ProductKey: "modis:c61", SourceID: "modis", Satellite: "Aqua", DataType: "MYD03", Version: "6.1", OfficialID: "c61"},
		{ProductKey: "modis:c7", SourceID: "modis", Satellite: "Aqua", DataType: "MYD03", Version: "7", OfficialID: "c7"},
		{ProductKey: "modis:terra", SourceID: "modis", Satellite: "Terra", DataType: "MYD03", Version: "7"},
		{ProductKey: "viirs:c7", SourceID: "viirs", Satellite: "Aqua", DataType: "MYD03", Version: "7"},
		{ProductKey: "modis:MYD06", SourceID: "modis", Satellite: "Aqua", DataType: "MYD06_L2", Version: "7"},
	}
	got := catalogVersionAlternatives(products)
	if len(got) != 2 {
		t.Fatalf("unrelated sources/products linked as versions: %+v", got)
	}
	want := []collectionVersion{{ProductKey: "modis:c7", Version: "7", OfficialID: "c7"}}
	if !reflect.DeepEqual(got["modis:c61"], want) {
		t.Fatalf("C6.1 version candidates: %+v", got["modis:c61"])
	}
	if !reflect.DeepEqual(got["modis:c7"], []collectionVersion{{ProductKey: "modis:c61", Version: "6.1", OfficialID: "c61"}}) {
		t.Fatalf("C7 version candidates: %+v", got["modis:c7"])
	}
}

func TestCatalogueVersionAlternativesKeepDistinctUnversionedCollections(t *testing.T) {
	products := []model.Product{
		{ProductKey: "viirs:old", SourceID: "viirs", Satellite: "Suomi NPP", DataType: "AERDB", Version: ""},
		{ProductKey: "viirs:new", SourceID: "viirs", Satellite: "Suomi NPP", DataType: "AERDB", Version: "2.1"},
	}
	got := catalogVersionAlternatives(products)
	if len(got["viirs:old"]) != 1 || got["viirs:old"][0].ProductKey != "viirs:new" {
		t.Fatalf("versionless variant should remain addressable by collection ID: %+v", got)
	}
}

func TestDownloadCatalogueExposesDatedModesAndVersionAlternatives(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		source string
		items  []model.Product
	}{
		{"goes16", []model.Product{{ProductKey: "goes16:ABI-L1b-RadF", SourceID: "goes16", DataType: "ABI-L1b-RadF", Satellite: "GOES-16", Cadence: "10m"}}},
		{"modis", []model.Product{
			{ProductKey: "modis:C1379841358-LAADS", SourceID: "modis", DataType: "MYD03", Satellite: "Aqua", Version: "6.1"},
			{ProductKey: "modis:C3860236666-LAADS", SourceID: "modis", DataType: "MYD03", Satellite: "Aqua", Version: "7"},
		}},
	} {
		if err := store.UpsertProducts(t.Context(), tc.source, tc.items); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	New(registry, store, nil).ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/downloads/catalog", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Products []struct {
			ProductKey  string `json:"product_key"`
			TimeProfile struct {
				ObservedScanModes []struct {
					Date string `json:"date"`
					Mode string `json:"mode"`
				} `json:"observed_scan_modes"`
			} `json:"time_profile"`
			VersionAlternatives []collectionVersion `json:"version_alternatives"`
		} `json:"products"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Products) != 3 {
		t.Fatalf("catalog expected three entries, got %+v", payload.Products)
	}
	for _, item := range payload.Products {
		switch item.ProductKey {
		case "goes16:ABI-L1b-RadF":
			if len(item.TimeProfile.ObservedScanModes) != 3 || item.TimeProfile.ObservedScanModes[0].Mode != "M3" || len(item.VersionAlternatives) != 0 {
				t.Fatalf("incorrect historical scan observations: %+v", item)
			}
		case "modis:C1379841358-LAADS":
			if len(item.VersionAlternatives) != 1 || item.VersionAlternatives[0].ProductKey != "modis:C3860236666-LAADS" {
				t.Fatalf("collection6.1 lost collection7 alternative: %+v", item)
			}
		case "modis:C3860236666-LAADS":
			if len(item.VersionAlternatives) != 1 || item.VersionAlternatives[0].Version != "6.1" {
				t.Fatalf("collection7 lost collection6.1 alternative: %+v", item)
			}
		default:
			t.Fatalf("unexpected entry: %+v", item)
		}
	}
}
