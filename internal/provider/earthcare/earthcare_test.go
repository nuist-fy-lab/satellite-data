package earthcare

import "testing"

func TestScienceAssetsPrefersIndividualDataFiles(t *testing.T) {
	assets := scienceAssets(map[string]map[string]any{
		"thumbnail": {
			"href":  "https://example.invalid/preview.jpg",
			"roles": []any{"thumbnail"},
		},
		"product": {
			"href":            "https://example.invalid/product.zip",
			"roles":           []any{"data", "metadata", "archive"},
			"file:local_path": "product.zip",
		},
		"enclosure_h5": {
			"href":            "https://example.invalid/product.h5",
			"roles":           []any{"data"},
			"file:local_path": "product.h5",
			"file:size":       float64(42),
			"type":            "application/x-hdf5",
		},
	})
	if len(assets) != 1 {
		t.Fatalf("expected one preferred science asset, got %d", len(assets))
	}
	if assets[0].Name != "product.h5" || assets[0].Size != 42 || assets[0].AuthRef != "earthcare" {
		t.Fatalf("unexpected asset: %#v", assets[0])
	}
}

func TestEarthCAREProductTypesFromOfficialCategories(t *testing.T) {
	properties := map[string]any{
		"categories": []any{
			map[string]any{"scheme": "http://www.opengis.net/eop/2.1/productType", "label": "ATL_CLA_2A"},
			map[string]any{"scheme": "http://www.opengis.net/eop/2.1/productType", "label": "CPR_CLP_2A"},
			map[string]any{"scheme": "https://earth.esa.int/concepts/concept_scheme/instruments", "label": "ATLID"},
		},
	}
	got := earthcareProductTypes(properties)
	if len(got) != 2 || got[0] != "ATL_CLA_2A" || got[1] != "CPR_CLP_2A" {
		t.Fatalf("unexpected product types: %#v", got)
	}
	if instrument := earthcareInstrument("MSI_COP_2A"); instrument != "MSI" {
		t.Fatalf("instrument=%q", instrument)
	}
	if level := earthcareProductLevel("MSI_COP_2A"); level != "L2A" {
		t.Fatalf("level=%q", level)
	}
}
