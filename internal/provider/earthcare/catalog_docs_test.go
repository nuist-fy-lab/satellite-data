package earthcare

import "testing"

func TestProductDocumentationMatchesInstrumentAndProductType(t *testing.T) {
	properties := map[string]any{"links": map[string]any{"describedby": []any{
		map[string]any{"title": "EarthCARE ATLID Level 1 Product Definition Document", "href": "https://example.test/atlid-l1.pdf"},
		map[string]any{"title": "EarthCARE MSI Level 1 Product Definition Document", "href": "https://example.test/msi-l1.pdf"},
		map[string]any{"title": "EarthCARE ATLID CTH Level 2A Product Definition Document", "href": "https://example.test/atlid-cth.pdf"},
		map[string]any{"title": "EarthCARE ATLID/MSI Level 2B CTH Product Definition Document", "href": "https://example.test/synergy-cth.pdf"},
	}}}
	for _, item := range []struct{ product, want string }{{"ATL_NOM_1B", "https://example.test/atlid-l1.pdf"}, {"MSI_RAD_1C", "https://example.test/msi-l1.pdf"}, {"ATL_CTH_2A", "https://example.test/atlid-cth.pdf"}, {"AM__CTH_2B", "https://example.test/synergy-cth.pdf"}, {"ACM_CAP_2B", ""}, {"AUX_CCOG_B", ""}} {
		if got := earthcareDocumentation(properties, item.product); got != item.want {
			t.Errorf("%s documentation=%s, want %s", item.product, got, item.want)
		}
	}
}
