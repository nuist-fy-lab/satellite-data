package eumetsat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
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

func TestIASICatalogClassification(t *testing.T) {
	if !isIASICollection("IASI Level 1C - All Spectral Samples - Metop - Global") {
		t.Fatal("first-generation IASI collection was not recognized")
	}
	if isIASICollection("Global IASI-NG Level 1c Spectral Radiance - Metop-SG") {
		t.Fatal("IASI-NG was mixed into the first-generation IASI source")
	}
	tests := []struct {
		id, title, satellite, instrument, level, area, productType string
	}{
		{"EO:EUM:DAT:METOP:IASIL1C-ALL", "IASI Level 1C - All Spectral Samples - Metop - Global", "Metop", "IASI", "L1c", "ORBIT", "All spectral samples"},
		{"EO:EUM:DAT:METOP:IASPCS01", "IASI Level 1 Principal Component Scores - Metop - Global", "Metop", "IASI", "L1", "ORBIT", "Principal component scores"},
		{"EO:EUM:DAT:METOP:IASIL2COX", "IASI Carbon Monoxide Profiles FORLI-CO - Metop", "Metop-B / Metop-C", "IASI", "L2", "SWATH", "Carbon monoxide profiles"},
		{"EO:EUM:DAT:METOP:IASSND02", "IASI Combined Sounding Products - Metop", "Metop", "IASI", "L2", "ORBIT", "Combined sounding products"},
		{"EO:EUM:DAT:0959", "IASI Carbon Monoxide Profiles FORLI-CO Climate Data Record Release 1 - Metop-A and -B", "Metop-A / Metop-B", "IASI", "L2", "ORBIT", "Carbon monoxide profiles"},
		{"EO:EUM:DAT:0960", "IASI SO2 Climate Data Record Release 1 - Metop-A and -B", "Metop-A / Metop-B", "IASI", "L2", "ORBIT", "Sulphur dioxide profiles"},
		{"EO:EUM:DAT:0576", "IASI All Sky Temperature and Humidity Profiles - Climate Data Record Release 1.1 - Metop-A and -B", "Metop-A / Metop-B", "IASI", "L2", "ORBIT", "All-sky temperature and humidity profiles"},
		{"EO:EUM:DAT:0758", "IASI Principal Components Scores Fundamental Data Record Release 1 - Metop-A and -B", "Metop-A / Metop-B", "IASI", "L1", "ORBIT", "Principal component scores"},
		{"EO:EUM:DAT:1027", "IASI Ozone (O3) Climate Data Record Release 1 - Metop-A and -B", "Metop-A / Metop-B", "IASI", "L2", "ORBIT", "Ozone profiles"},
		{"EO:EUM:DAT:0590", "IASI Level 1C Climate Data Record Release 2 - Metop-A and -B", "Metop-A / Metop-B", "IASI", "L1c", "ORBIT", "Level 1C climate data record"},
		{"EO:EUM:DAT:1099", "IASI Noise Covariance Matrix - Metop", "Metop", "Auxiliary", "AUX", "AUX", "Noise covariance matrix"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			collection := iasiCollection(tc.id, tc.title, "https://api.eumetsat.int/catalog", 123)
			if collection.SourceID != "iasi" || collection.Satellite != tc.satellite || collection.Instrument != tc.instrument || collection.Level != tc.level || collection.Metadata["area_type"] != tc.area || collection.Metadata["product_type"] != tc.productType {
				t.Fatalf("unexpected collection classification: %+v", collection)
			}
			if collection.Cadence != "event" {
				t.Fatalf("IASI collection cadence must remain event-based: %+v", collection)
			}
		})
	}
	if got := iasiVersion("IASI SO2 Climate Data Record Release 1.1 - Metop-A and -B"); got != "R1.1" {
		t.Fatalf("version=%q", got)
	}
	if got := iasiCollection("EO:EUM:DAT:METOP:IASIL2COX", "IASI Carbon Monoxide Profiles FORLI-CO - Metop", "", 1).Metadata["platforms"]; got != "Metop-B, Metop-C" {
		t.Fatalf("active L2 platform coverage=%q", got)
	}
}

func TestIASIProductPlatformAndAncillaryTime(t *testing.T) {
	for _, tc := range []struct{ productID, want string }{
		{"IASI_xxx_1C_M01_20250606225057Z", "Metop-B"},
		{"IASI_xxx_1C_M02_20250606225057Z", "Metop-A"},
		{"IASI_xxx_1C_M03_20250606225057Z", "Metop-C"},
		{"W_XX-EUMETSAT-Darmstadt,SOUNDING+SATELLITE,METOPC+IASI_C_EUMR_20250607000252", "Metop-C"},
	} {
		if got := iasiSatelliteFromProductID(tc.productID); got != tc.want {
			t.Errorf("iasiSatelliteFromProductID(%q)=%q want %q", tc.productID, got, tc.want)
		}
	}
	productID := "IASI_NCM_1C_M02_PN01_20250917085819Z.nc.bz2"
	start, end, ok := parseIASINoiseCovarianceTime(productID)
	want := time.Date(2025, 9, 17, 8, 58, 19, 0, time.UTC)
	if !ok || !start.Equal(want) || !end.Equal(want) {
		t.Fatalf("NCM timestamp start=%s end=%s ok=%v", start, end, ok)
	}
}

func TestIASISearchUsesAcquisitionMetadataAndOrbitCoverage(t *testing.T) {
	productID := "IASI_xxx_1C_M01_20250606225057Z_20250607003257Z_N_O_20250606234204Z"
	ncmProductID := "IASI_NCM_1C_M02_PN01_20250917085819Z.nc.bz2"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/data/browse/1.0.0/collections":
			_ = json.NewEncoder(w).Encode(map[string]any{"links": []any{
				map[string]any{"rel": "collection/details", "title": "EO:EUM:DAT:METOP:IASIL1C-ALL", "datasetTitle": "IASI Level 1C - All Spectral Samples - Metop - Global", "href": "https://api.eumetsat.int/data/browse/collections/EO%3AEUM%3ADAT%3AMETOP%3AIASIL1C-ALL", "numberOfProducts": 95573},
				map[string]any{"rel": "collection/details", "title": "EO:EUM:DAT:1099", "datasetTitle": "IASI Noise Covariance Matrix - Metop", "numberOfProducts": 12},
				map[string]any{"rel": "collection/details", "title": "EO:EUM:DAT:0442", "datasetTitle": "Global IASI-NG Level 1c Spectral Radiance (netCDF) - Metop-SG", "numberOfProducts": 1},
			}})
		case r.URL.Path == "/data/search-products/1.0.0/os":
			collectionID := r.URL.Query().Get("pi")
			if collectionID == "EO:EUM:DAT:1099" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"totalResults": 1,
					"features": []any{map[string]any{
						"id": ncmProductID,
						"properties": map[string]any{
							"date":                   "2007-05-15T00:00:00Z/2099-01-01T00:00:00Z",
							"acquisitionInformation": []any{map[string]any{"platform": map[string]any{"platformShortName": "Metop-A"}}},
							"links": map[string]any{"data": []any{map[string]any{
								"href":      "https://api.eumetsat.int/data/download/1.0.0/collections/EO%3AEUM%3ADAT%3A1099/products/" + ncmProductID,
								"mediaType": "application/zip",
							}}},
						},
					}},
				})
				return
			}
			if collectionID != "EO:EUM:DAT:METOP:IASIL1C-ALL" {
				t.Errorf("unexpected collection query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalResults": 1,
				"features": []any{map[string]any{
					"id": productID,
					"properties": map[string]any{
						"date":    "2025-06-06T22:50:57Z/2025-06-07T00:32:57Z",
						"updated": "2025-06-07T01:28:26.018Z",
						"acquisitionInformation": []any{map[string]any{
							"platform":              map[string]any{"platformShortName": "Metop-B"},
							"instrument":            map[string]any{"instrumentShortName": "IASI"},
							"acquisitionParameters": map[string]any{"orbitNumber": 65995},
						}},
						"productInformation": map[string]any{"productType": "IASxxx1C"},
						"links": map[string]any{"data": []any{map[string]any{
							"href":      "https://api.eumetsat.int/data/download/1.0.0/collections/EO%3AEUM%3ADAT%3AMETOP%3AIASIL1C-ALL/products/" + productID,
							"mediaType": "application/zip",
						}}},
					},
				}},
			})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := NewIASI(&http.Client{Transport: &rewriteEUMETSATHost{target: server.Client().Transport, host: strings.TrimPrefix(server.URL, "https://")}})
	collections, err := p.Collections(t.Context())
	if err != nil || len(collections) != 2 {
		t.Fatalf("IASI Browse API collections=%d err=%v", len(collections), err)
	}
	for _, collection := range collections {
		if strings.Contains(strings.ToUpper(collection.Metadata["title"]), "IASI-NG") {
			t.Fatalf("IASI-NG entered the first-generation source: %+v", collection)
		}
	}
	start := time.Date(2025, 6, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	granules, err := p.Search(t.Context(), model.Query{CollectionID: "iasi:EO:EUM:DAT:METOP:IASIL1C-ALL", Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
	if len(granules) != 1 {
		t.Fatalf("granules=%d want 1", len(granules))
	}
	granule := granules[0]
	wantStart := time.Date(2025, 6, 6, 22, 50, 57, 0, time.UTC)
	wantEnd := time.Date(2025, 6, 7, 0, 32, 57, 0, time.UTC)
	if granule.SourceID != "iasi" || granule.Metadata["satellite"] != "Metop-B" || granule.Metadata["instrument"] != "IASI" || granule.Metadata["orbit_number"] != "65995" || granule.Metadata["product_type"] != "IASxxx1C" {
		t.Fatalf("granule metadata=%+v", granule.Metadata)
	}
	if !granule.Start.Equal(wantStart) || !granule.End.Equal(wantEnd) || len(granule.Assets) != 1 || granule.Assets[0].AuthRef != "eumetsat" || granule.Assets[0].Name != productID+".zip" {
		t.Fatalf("unexpected IASI orbit granule: %+v", granule)
	}
	ncmStart := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	ncmEnd := ncmStart.Add(24 * time.Hour)
	granules, err = p.Search(t.Context(), model.Query{CollectionID: "iasi:EO:EUM:DAT:1099", Start: ncmStart, End: ncmEnd})
	if err != nil || len(granules) != 0 {
		t.Fatalf("NCM product outside the requested date range was returned: granules=%d err=%v", len(granules), err)
	}
	ncmStart = time.Date(2025, 9, 17, 0, 0, 0, 0, time.UTC)
	ncmEnd = ncmStart.Add(24 * time.Hour)
	granules, err = p.Search(t.Context(), model.Query{CollectionID: "iasi:EO:EUM:DAT:1099", Start: ncmStart, End: ncmEnd})
	if err != nil || len(granules) != 1 || granules[0].Metadata["satellite"] != "Metop-A" {
		t.Fatalf("NCM filename time/platform were not applied: granules=%+v err=%v", granules, err)
	}
}

type rewriteEUMETSATHost struct {
	target http.RoundTripper
	host   string
}

func (r *rewriteEUMETSATHost) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Host = r.host
	return r.target.RoundTrip(copy)
}

func TestIASINGSearchUsesThePublishedGlobalL1CCollection(t *testing.T) {
	productID := "W_XX-EUMETSAT-Darmstadt,SAT,SGA1-IAS-1C-RAD_C_EUMT_20261007075432_G_O_20261007064237_20261007064354_C_N_T__"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data/browse/1.0.0/collections":
			_ = json.NewEncoder(w).Encode(map[string]any{"links": []any{
				map[string]any{"rel": "collection/details", "title": "EO:EUM:DAT:0442", "datasetTitle": "Global IASI-NG Level 1c Spectral Radiance (netCDF) - Metop-SG", "href": "https://api.eumetsat.int/data/browse/1.0.0/collections/EO%3AEUM%3ADAT%3A0442?format=json", "numberOfProducts": 38780},
				map[string]any{"rel": "collection/details", "title": "EO:EUM:DAT:METOP:IASIL1C-ALL", "datasetTitle": "IASI Level 1C - All Spectral Samples - Metop - Global", "numberOfProducts": 95577},
				map[string]any{"rel": "collection/details", "title": "EO:EUM:DAT:0436", "datasetTitle": "Global 3MI L1b radiance data (netCDF) - Metop-SG", "numberOfProducts": 4707},
			}})
		case "/data/search-products/1.0.0/os":
			if r.URL.Query().Get("pi") != "EO:EUM:DAT:0442" {
				t.Errorf("unexpected IASI-NG collection query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalResults": 1,
				"features": []any{map[string]any{
					"id": productID,
					"properties": map[string]any{
						"date":    "2026-10-07T06:42:37Z/2026-10-07T06:43:54Z",
						"updated": "2026-10-07T09:33:43.122Z",
						"acquisitionInformation": []any{map[string]any{
							"platform":              map[string]any{"platformShortName": "SGA1"},
							"instrument":            map[string]any{"instrumentShortName": "IAS"},
							"acquisitionParameters": map[string]any{"orbitNumber": 5971},
						}},
						"productInformation": map[string]any{"productType": "IASRAD1C", "size": 69200},
						"links": map[string]any{"data": []any{map[string]any{
							"href":      "https://api.eumetsat.int/data/download/1.0.0/collections/EO%3AEUM%3ADAT%3A0442/products/" + productID,
							"mediaType": "application/zip",
						}}},
					},
				}},
			})
		default:
			t.Errorf("unexpected IASI-NG request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := NewIASING(&http.Client{Transport: &rewriteEUMETSATHost{target: server.Client().Transport, host: strings.TrimPrefix(server.URL, "https://")}})
	collections, err := p.Collections(t.Context())
	if err != nil || len(collections) != 1 {
		t.Fatalf("IASI-NG collections=%d err=%v", len(collections), err)
	}
	collection := collections[0]
	if collection.ID != "iasi-ng:EO:EUM:DAT:0442" || collection.SourceID != "iasi-ng" || collection.Satellite != "Metop-SG" || collection.Instrument != "IASI-NG" || collection.Level != "L1c" || collection.Metadata["area_type"] != "GLOBAL" || collection.Metadata["platforms"] != "Metop-SG-A1" || collection.Metadata["product_count"] != "38780" || collection.Metadata["spectral_samples"] != "16921" {
		t.Fatalf("unexpected IASI-NG collection: %+v", collection)
	}
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	granules, err := p.Search(t.Context(), model.Query{CollectionID: collection.ID, Start: start, End: end})
	if err != nil || len(granules) != 1 {
		t.Fatalf("IASI-NG granules=%d err=%v", len(granules), err)
	}
	granule := granules[0]
	wantStart := time.Date(2026, 10, 7, 6, 42, 37, 0, time.UTC)
	wantEnd := time.Date(2026, 10, 7, 6, 43, 54, 0, time.UTC)
	if granule.ID != "iasi-ng:"+productID || granule.Metadata["satellite"] != "Metop-SG-A1" || granule.Metadata["instrument"] != "IASI-NG" || granule.Metadata["orbit_number"] != "5971" || granule.Metadata["product_type"] != "IASRAD1C" {
		t.Fatalf("unexpected IASI-NG granule metadata: %+v", granule.Metadata)
	}
	if !granule.Start.Equal(wantStart) || !granule.End.Equal(wantEnd) || len(granule.Assets) != 1 || granule.Assets[0].AuthRef != "eumetsat" || granule.Assets[0].Name != productID+".zip" {
		t.Fatalf("unexpected IASI-NG granule: %+v", granule)
	}
}

func TestIASINGCollectionClassification(t *testing.T) {
	if !isIASINGCollection("Global IASI-NG Level 1c Spectral Radiance - Metop-SG") {
		t.Fatal("IASI-NG collection was not recognized")
	}
	if isIASINGCollection("IASI Level 1C - All Spectral Samples - Metop - Global") {
		t.Fatal("first-generation IASI collection entered IASI-NG")
	}
	if got := iasiNGLevel("Global IASI-NG Level 1d Principal Components - Metop-SG"); got != "L1d" {
		t.Fatalf("IASI-NG L1D level=%q", got)
	}
}
