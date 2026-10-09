package httpapi

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestCompactCoverageConditionalCompressionAndRefresh(t *testing.T) {
	ctx := t.Context()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m", AreaType: "DISK"}
	if err := store.UpsertProducts(ctx, product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	var firstName, firstSlot string
	for day := 1; day <= 5; day++ {
		at := time.Date(2023, 6, day, 0, 0, 21, 0, time.UTC)
		assets := make([]model.Asset, 0, 16)
		for band := 1; band <= 16; band++ {
			name := fmt.Sprintf("OR_ABI-L1b-RadF-M6C%02d_G18_%02d.nc", band, day)
			assets = append(assets, model.Asset{Name: name, Size: 10})
			if day == 1 && band == 6 {
				firstName, firstSlot = name, at.Format(time.RFC3339)
			}
		}
		granule := model.Granule{ID: fmt.Sprintf("g%d", day), Start: at, Assets: assets}
		if err := store.SaveOfficialBatch(ctx, state.WorkBatch{Product: product.ProductKey, Days: []string{at.Format(time.DateOnly)}}, []model.Granule{granule}); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(nil, store)
	urlYear := "/api/coverage/year?product=" + url.QueryEscape(product.ProductKey) + "&year=2023"
	request := func(path, accept, match string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if accept != "" {
			r.Header.Set("Accept-Encoding", accept)
		}
		if match != "" {
			r.Header.Set("If-None-Match", match)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	raw := request(urlYear, "", "")
	if raw.Code != http.StatusOK || raw.Header().Get("Content-Encoding") != "" {
		t.Fatalf("raw response: %d %v", raw.Code, raw.Header())
	}
	compressed := request(urlYear, "gzip", "")
	if compressed.Code != http.StatusOK || compressed.Header().Get("Content-Encoding") != "gzip" ||
		compressed.Header().Get("Vary") != "Accept-Encoding" || compressed.Body.Len() >= raw.Body.Len() {
		t.Fatalf("compression: code=%d raw=%d zipped=%d headers=%v", compressed.Code, raw.Body.Len(), compressed.Body.Len(), compressed.Header())
	}
	reader, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(decoded) != raw.Body.String() {
		t.Fatalf("compressed body mismatch: %v", err)
	}
	var payload state.CoverageYear
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	// Opening the year now requests low-priority indexing for the remaining
	// unknown days rather than leaving most of the calendar permanently gray.
	if payload.Version != 1 || len(payload.Bands) != 16 || len(payload.Days) != 365 {
		t.Fatalf("year auto-index did not include unknown dates: version=%d bands=%d days=%d", payload.Version, len(payload.Bands), len(payload.Days))
	}
	var indexing int
	for _, day := range payload.Days {
		if len(day) > 10 {
			if active, ok := day[10].(float64); ok && active > 0 {
				indexing++
			}
		}
	}
	if indexing != 360 {
		t.Fatalf("expected 360 missing dates queued for indexing, got %d", indexing)
	}
	etag := compressed.Header().Get("ETag")
	if etag == "" || compressed.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("cache headers: %v", compressed.Header())
	}
	notModified := request(urlYear, "gzip", etag)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional response: %d %q", notModified.Code, notModified.Body.String())
	}
	month := request("/api/coverage/month?product="+url.QueryEscape(product.ProductKey)+"&year=2023&month=6&band=6&time=hourly", "", "")
	if month.Code != http.StatusOK || !strings.Contains(month.Body.String(), `"month":6`) {
		t.Fatalf("month response: %d", month.Code)
	}
	file := request("/api/coverage/files?product="+url.QueryEscape(product.ProductKey)+"&band=6&slot="+url.QueryEscape(firstSlot), "", "")
	if file.Code != http.StatusOK || !strings.Contains(file.Body.String(), firstName) {
		t.Fatalf("file response: %d %q", file.Code, file.Body.String())
	}
	if _, err := store.ReplaceLocalFiles(ctx, []state.LocalFile{{Path: "/archive/" + firstName, Filename: firstName, Size: 10, MtimeNS: 1}}, "new"); err != nil {
		t.Fatal(err)
	}
	changed := request(urlYear, "gzip", etag)
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Fatalf("stale ETag: %d %v", changed.Code, changed.Header())
	}
}
