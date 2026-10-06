package provider

import (
	"encoding/csv"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestNormalizeProcessingLevels(t *testing.T) {
	for input, want := range map[string]string{
		"0": "L0", "1B": "L1B", "L1b": "L1B", "level 1c": "L1C", "1.5": "L1.5", "2G": "L2G", "3S": "L3S",
		"L0 / L1": "L0/L1", "aux": "AUX", "orbit": "ORBIT", "pressure-levels": "pressure-levels", "single-levels": "single-levels", "land": "land", "": "", "unknown": "unknown",
	} {
		if got := NormalizeLevel(input); got != want {
			t.Errorf("NormalizeLevel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExplicitSpatialUnits(t *testing.T) {
	for input, want := range map[string]string{
		"5-Min L2 Swath 3km": "3km", "Global 25km Equal Area": "25km", "6-Min Swath 6 km V2.1": "6 km",
		"1x1 degree grid": "1x1 degree", "0.25x0.25 Degree Grid": "0.25x0.25 Degree", "15 arc second Linear Lat Lon": "15 arc second",
		"L1 6-Min V2.1": "", "Aeronet Subsetting Product": "",
	} {
		if got := ResolutionFromTitle(input); got != want {
			t.Errorf("ResolutionFromTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCatalogNormalizationDoesNotMutateCachedMetadata(t *testing.T) {
	original := model.Product{ProductKey: "calipso:C123", OfficialID: "C123", SourceID: "calipso", Satellite: "CALIPSO", Instrument: "CALIOP", Level: "1B", DataType: "CAL_LID_L15-Standard-V1-01", DisplayName: "CALIPSO Lidar Level 1.5 Profile, V1-01", Version: "V1-01", Metadata: map[string]string{"title": "original"}}
	got := NormalizeProduct(original)
	if got.Level != "L1.5" || got.Metadata["original_level"] != "1B" {
		t.Fatalf("wrong level: %#v", got)
	}
	if got.ProductKey != original.ProductKey || got.OfficialID != original.OfficialID || got.Version != original.Version || got.DisplayName != original.DisplayName {
		t.Fatal("official identity changed")
	}
	if !reflect.DeepEqual(original.Metadata, map[string]string{"title": "original"}) {
		t.Fatal("mutated cached metadata")
	}
	if !reflect.DeepEqual(got, NormalizeProduct(got)) {
		t.Fatal("normalization is not idempotent")
	}
}

func TestNonImageProductsKeepTheirClassifications(t *testing.T) {
	cases := []struct {
		item                                            model.Product
		level, instrument, resolutionStatus, areaStatus string
	}{
		{model.Product{SourceID: "earthcare", DataType: "AUX_CCOG_B", Instrument: "Synergy"}, "AUX", "Auxiliary", "not_applicable", "not_applicable"},
		{model.Product{SourceID: "earthcare", DataType: "MPL_ORBSCT", Instrument: "Synergy"}, "ORBIT", "Orbit", "not_applicable", "not_applicable"},
		{model.Product{SourceID: "goes18", DataType: "SEIS-L1b-SGPS", Instrument: "SEIS", Level: "L1b"}, "L1B", "SEISS", "not_applicable", "not_applicable"},
		{model.Product{SourceID: "era5", DataType: "reanalysis-era5-pressure-levels", Instrument: "ERA5", Level: "pressure-levels", Resolution: "0.25deg", AreaType: "GLOBAL"}, "pressure-levels", "ERA5", "provided", "provided"},
	}
	for _, tc := range cases {
		got := NormalizeProduct(tc.item)
		if got.Level != tc.level || got.Instrument != tc.instrument || got.Metadata["resolution_status"] != tc.resolutionStatus || got.Metadata["area_type_status"] != tc.areaStatus {
			t.Fatalf("incorrect non-image classification: %#v", got)
		}
	}
}

func TestEverySnapshotProductHasFieldStatusAndEvidence(t *testing.T) {
	file, err := os.Open("../../docs/product-catalog-2026-09-27.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]int{}
	for i, name := range records[0] {
		header[name] = i
	}
	if len(records)-1 != 808 {
		t.Fatalf("unexpected snapshot size: %d", len(records)-1)
	}
	sources := map[string]bool{}
	for _, record := range records[1:] {
		field := func(key string) string { return record[header[key]] }
		original := model.Product{ProductKey: field("product_key"), OfficialID: field("official_id"), SourceID: field("source_id"), Satellite: field("satellite"), Instrument: field("instrument"), Level: field("level"), DataType: field("product"), DisplayName: field("title"), Cadence: field("cadence"), Resolution: field("resolution"), Version: field("version"), AreaType: field("area_type")}
		got := NormalizeProduct(original)
		sources[got.SourceID] = true
		if got.ProductKey != original.ProductKey || got.OfficialID != original.OfficialID || got.DataType != original.DataType || got.DisplayName != original.DisplayName || got.Cadence != original.Cadence {
			t.Fatalf("identity changed: %s", original.ProductKey)
		}
		if original.Resolution != "" && got.Resolution != original.Resolution || original.Version != "" && got.Version != original.Version {
			t.Fatalf("declared metadata changed: %s", original.ProductKey)
		}
		for _, name := range []string{"satellite", "instrument", "level", "resolution", "version", "area_type"} {
			if got.Metadata[name+"_status"] == "" || got.Metadata[name+"_evidence"] == "" {
				t.Fatalf("unlabeled field %s: %s", name, original.ProductKey)
			}
		}
		if !strings.HasPrefix(got.Metadata["official_product_url"], "https://") {
			t.Fatalf("missing product-specific official link: %s", got.ProductKey)
		}
		if got.SourceID == "mtg" && (!strings.HasPrefix(got.Level, "L") || got.Instrument == "MTG" || got.Instrument == "") {
			t.Fatalf("unresolved MTG classification: %#v", got)
		}
		if got.SourceID == "cloudsat" && got.Version == "" {
			t.Fatalf("unresolved CloudSat version: %s", got.ProductKey)
		}
		if !reflect.DeepEqual(got, NormalizeProduct(got)) {
			t.Fatalf("non-idempotent product: %s", got.ProductKey)
		}
	}
	if len(sources) != 13 {
		t.Fatalf("expected all 13 sources, got %d", len(sources))
	}
}
