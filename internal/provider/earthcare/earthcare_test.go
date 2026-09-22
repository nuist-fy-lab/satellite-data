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
