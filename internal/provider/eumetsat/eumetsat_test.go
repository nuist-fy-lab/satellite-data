package eumetsat

import (
	"testing"
	"time"
)

func TestParseDateProperty(t *testing.T) {
	start, end, ok := parseDateProperty("2026-09-21T00:00:07.000Z/2026-09-21T00:09:24.000Z")
	if !ok {
		t.Fatal("date property did not parse")
	}
	if want := time.Date(2026, 9, 21, 0, 0, 7, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start=%s want=%s", start, want)
	}
	if want := time.Date(2026, 9, 21, 0, 9, 24, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end=%s want=%s", end, want)
	}
}

func TestMTGCatalogClassification(t *testing.T) {
	if !isMTGCollection("FCI Level 1c Normal Resolution Image Data - MTG - 0 degree") {
		t.Fatal("MTG collection was not recognized")
	}
	if got := mtgInstrument("LI Lightning Flashes - MTG - 0 degree"); got != "LI" {
		t.Fatalf("instrument=%q", got)
	}
	if got := mtgLevel("FCI Level 1c High Resolution Image Data - MTG - 0 degree"); got != "L1c" {
		t.Fatalf("level=%q", got)
	}
	if got := mtgArea("Cloud Mask - MTG - 0 degree"); got != "DISK" {
		t.Fatalf("area=%q", got)
	}
}

func TestMTGLevelOneProductTypes(t *testing.T) {
	for _, tc := range []struct{ title, level, productType string }{
		{"FCI Level 1c Normal Resolution Image Data - MTG - 0 degree", "L1c", "FCI-1C-RRAD-FDHSI-FD (normal resolution)"},
		{"FCI Level 1c High Resolution Image Data - MTG - 0 degree", "L1c", "FCI-1C-RRAD-HRFI-FD (high resolution)"},
		{"IRS Level 1 reduced sub-set of 300 Principal Components - MTG", "L1", "IRS L1 - 300 principal components"},
		{"IRS Level 1 spectral soundings - MTG", "L1", "IRS L1 - spectral soundings"},
	} {
		if got := mtgLevel(tc.title); got != tc.level {
			t.Errorf("mtgLevel(%q) = %q, want %q", tc.title, got, tc.level)
		}
		if got := mtgProductType(tc.title); got != tc.productType {
			t.Errorf("mtgProductType(%q) = %q, want %q", tc.title, got, tc.productType)
		}
	}
	if got := mtgProductType("LI Lightning Events Filtered - MTG - 0 degree"); got != "" {
		t.Fatalf("non-L1 product mislabeled as %q", got)
	}
}

func TestProductDownloadLinkUsesOfficialHref(t *testing.T) {
	want := "https://api.eumetsat.int/data/download/1.0.0/collections/EO%3AEUM%3ADAT%3A0678/products/W_XX-EUMETSAT-Darmstadt%2CIMG%2BSAT"
	properties := map[string]any{
		"links": map[string]any{
			"data": []any{
				map[string]any{
					"href":      want,
					"mediaType": "application/zip",
				},
			},
		},
	}
	href, mediaType, ok := productDownloadLink(properties)
	if !ok || href != want || mediaType != "application/zip" {
		t.Fatalf("href=%q mediaType=%q ok=%v", href, mediaType, ok)
	}
}
