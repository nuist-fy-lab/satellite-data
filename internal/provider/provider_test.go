package provider

import (
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestProductFromCollectionUsesOfficialMetadata(t *testing.T) {
	source := model.Source{ID: "modis", Tier: model.TierToken}
	collection := model.Collection{
		ID:         "modis:C123-LAADS",
		SourceID:   "modis",
		Satellite:  "Terra",
		Instrument: "MODIS",
		Level:      "L2",
		Product:    "MOD06_L2",
		Cadence:    "5m",
		Metadata: map[string]string{
			"concept_id":         "C123-LAADS",
			"title":              "MODIS Terra Cloud Product",
			"version":            "6.1",
			"spatial_resolution": "1km",
		},
	}
	got := ProductFromCollection(source, collection)
	if got.ProductKey != collection.ID || got.OfficialID != "C123-LAADS" {
		t.Fatalf("unexpected ids: %#v", got)
	}
	if got.DisplayName != "MODIS Terra Cloud Product" || got.Resolution != "1km" || got.Version != "6.1" {
		t.Fatalf("unexpected normalized metadata: %#v", got)
	}
}
