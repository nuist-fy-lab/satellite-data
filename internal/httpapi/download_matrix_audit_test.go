package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/producttime"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestDownloadMatrixAudit(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	products := []model.Product{
		{ProductKey: "modis:daily", SourceID: "modis", Cadence: "1d", Version: "6"},
		{ProductKey: "modis:monthly", SourceID: "modis", Cadence: "1mo", Version: "6.2"},
		{ProductKey: "modis:annual", SourceID: "modis", Cadence: "1y"},
		{ProductKey: "modis:dekad", SourceID: "modis", Cadence: "dekad"},
		{ProductKey: "modis:weekly", SourceID: "modis", Cadence: "1w"},
		{ProductKey: "modis:eight", SourceID: "modis", Cadence: "8d"},
		{ProductKey: "modis:ten", SourceID: "modis", Cadence: "10d"},
		{ProductKey: "modis:sixteen", SourceID: "modis", Cadence: "16d"},
		{ProductKey: "modis:orbit", SourceID: "modis", Cadence: "5m", AreaType: "ORBIT"},
	}
	for i := 0; i < 33; i++ {
		products = append(products, model.Product{ProductKey: fmt.Sprintf("modis:p%02d", i), SourceID: "modis", Cadence: "1d"})
	}
	if err := s.UpsertProducts(t.Context(), "modis", products); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProducts(t.Context(), "goes18", []model.Product{{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}}); err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	h := New(registry, s)
	call := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return w
	}
	for _, tc := range []struct {
		name, product, start, end string
		status                    int
	}{
		{"leap day", "daily", "2024-02-29", "2024-03-01", 202},
		{"invalid leap day", "daily", "2023-02-29", "2023-03-01", 400},
		{"daily BJT midnight not UTC", "daily", "2024-02-29T00:00:00+08:00", "2024-03-01T00:00:00+08:00", 400},
		{"daily BJT UTC boundary", "daily", "2024-02-29T08:00:00+08:00", "2024-03-01T08:00:00+08:00", 202},
		{"month leap", "monthly", "2024-02-01", "2024-03-01", 202},
		{"partial month", "monthly", "2024-02-02", "2024-03-01", 400},
		{"year", "annual", "2024-01-01", "2025-01-01", 202},
		{"partial year", "annual", "2024-02-01", "2025-01-01", 400},
		{"leap final dekad", "dekad", "2024-02-21", "2024-03-01", 202},
		{"partial dekad", "dekad", "2024-02-20", "2024-03-01", 400},
		{"weekly custom dates", "weekly", "2024-12-30", "2025-01-02", 202},
		{"eight custom dates", "eight", "2024-12-30", "2025-01-02", 202},
		{"ten custom dates", "ten", "2024-12-30", "2025-01-02", 202},
		{"sixteen custom dates", "sixteen", "2024-12-30", "2025-01-02", 202},
		{"empty half open", "daily", "2024-03-01", "2024-03-01", 400},
		{"reverse interval", "daily", "2024-03-02", "2024-03-01", 400},
		{"malformed timestamp", "orbit", "2024-01-01T25:00:00Z", "2024-01-02T00:00:00Z", 400},
		{"invalid UTC offset hour", "orbit", "2024-01-01T00:00:00+24:00", "2024-01-02T00:00:00+24:00", 400},
		{"invalid UTC offset minute", "orbit", "2024-01-01T00:00:00+08:60", "2024-01-02T00:00:00+08:60", 400},
		{"zero start", "daily", "0001-01-01", "0001-01-02", 400},
		{"maximum date", "daily", "9998-01-01", "9999-01-01", 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"products":["modis:%s"],"start":%q,"end":%q}`, tc.product, tc.start, tc.end)
			w := call("/api/v1/download-tasks", body)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	var keys []string
	for i := 0; i < 33; i++ {
		keys = append(keys, fmt.Sprintf("modis:p%02d", i))
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"32 products", fmt.Sprintf(`{"products":%s,"start":"2024-01-01","end":"2024-01-02"}`, jsonStrings(keys[:32])), 202},
		{"33 products", fmt.Sprintf(`{"products":%s,"start":"2024-01-01","end":"2024-01-02"}`, jsonStrings(keys)), 400},
		{"duplicate product", `{"products":["modis:daily","modis:daily"],"start":"2024-01-01","end":"2024-01-02"}`, 400},
		{"unknown option product", `{"products":["modis:daily"],"start":"2024-01-01","end":"2024-01-02","options":{"modis:monthly":{}}}`, 400},
		{"unknown option field", `{"products":["modis:daily"],"start":"2024-01-01","end":"2024-01-02","options":{"modis:daily":{"typo":1}}}`, 400},
		{"invalid second product", `{"products":["modis:daily","modis:monthly"],"start":"2024-02-02","end":"2024-03-01"}`, 400},
		{"recurring off-grid scan", `{"products":["goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","times":["00:03"]}`, 400},
		{"historical five-minute scan selection", `{"products":["goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","times":["00:05"]}`, 202},
		{"off-grid scene rejected", `{"products":["goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","scenes":{"2024-01-01":["00:03"]}}`, 400},
		{"orbit scene accepted", `{"products":["modis:orbit"],"start":"2024-01-01","end":"2024-01-02","scenes":{"2024-01-01":["00:05"]}}`, 202},
		{"scene on exclusive end", `{"products":["goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","scenes":{"2024-01-02":["00:00"]}}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := s.DownloadRequests(t.Context(), 1000)
			if err != nil {
				t.Fatal(err)
			}
			w := call("/api/v1/download-tasks", tc.body)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			after, err := s.DownloadRequests(t.Context(), 1000)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status >= 400 && len(before) != len(after) {
				t.Fatalf("rejected request persisted %d orders", len(after)-len(before))
			}
		})
	}
	// Legacy groups accept partial periods; this is intentionally recorded rather than assumed equivalent.
	w := call("/api/v1/downloads/batch", `{"products":["modis:monthly"],"start":"2024-02-02","end":"2024-03-01"}`)
	if w.Code != 202 {
		t.Fatalf("legacy group: %d %s", w.Code, w.Body.String())
	}
	w = call("/api/v1/download-tasks", `{"products":["goes18:ABI-L1b-RadF","modis:daily"],"start":"2024-01-01","end":"2024-01-02","times":["00:00"],"options":{"modis:daily":{"times":[],"start":"2024-02-29","end":"2024-03-01"}}}`)
	var result struct {
		Task downloadTaskView `json:"task"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Task.Downloads) != 2 {
		t.Fatalf("inheritance: %d %s", w.Code, w.Body.String())
	}
	for _, item := range result.Task.Downloads {
		if item.CollectionID == "modis:daily" && (len(item.Selection.Times) != 0 || item.Start.Format("2006-01-02") != "2024-02-29" || item.Version != "6") {
			t.Fatalf("override: %+v", item)
		}
		if item.CollectionID == "goes18:ABI-L1b-RadF" && (len(item.Selection.Times) != 1 || item.Selection.Times[0] != "00:00") {
			t.Fatalf("inheritance: %+v", item)
		}
	}
}

func jsonStrings(values []string) string { b, _ := json.Marshal(values); return string(b) }

// The CSV is a historical catalog snapshot, not a claim that products are currently available.
func TestDownloadSnapshotMatrixAudit(t *testing.T) {
	f, err := os.Open("../../docs/product-catalog-2026-09-27.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bySource := map[string][]model.Product{}
	for _, row := range rows[1:] {
		p := model.Product{SourceID: row[0], ProductKey: row[1], Satellite: row[3], Instrument: row[4], Level: row[5], DataType: row[6], DisplayName: row[7], Cadence: row[8], AreaType: row[9], Version: row[11], Metadata: map[string]string{"availability_mode": row[15]}}
		bySource[p.SourceID] = append(bySource[p.SourceID], p)
	}
	for source, products := range bySource {
		if err := s.UpsertProducts(t.Context(), source, products); err != nil {
			t.Fatalf("%s: %v", source, err)
		}
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	h := New(registry, s)
	counts := map[string]int{}
	for source, products := range bySource {
		for _, p := range products {
			profile := producttime.Resolve(provider.NormalizeProduct(p))
			counts[source+"/"+profile.Kind]++
			body, err := json.Marshal(map[string]any{"products": []string{p.ProductKey}, "start": "2024-01-01", "end": "2025-01-01"})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/download-tasks", strings.NewReader(string(body))))
			want := http.StatusAccepted
			// On-demand products require explicit CDS inputs; none are submitted here.
			if source == "era5" {
				want = http.StatusBadRequest
			}
			if w.Code != want {
				t.Errorf("%s profile=%s: got %d want %d: %s", p.ProductKey, profile.Kind, w.Code, want, w.Body.String())
			}
		}
	}
	t.Logf("historical snapshot source/profile counts: %v", counts)
}

func TestDownloadOwnerDedupMatrixAudit(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth, err := authstore.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	keys := map[string]string{}
	for _, name := range []string{"alice", "bob"} {
		u, err := auth.CreateUser(t.Context(), name, "test-password-1234", "user")
		if err != nil {
			t.Fatal(err)
		}
		_, keys[name], err = auth.CreateAPIKey(t.Context(), u.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	for source, products := range map[string][]model.Product{
		"modis":  {{ProductKey: "modis:daily", SourceID: "modis", Cadence: "1d"}},
		"goes18": {{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}},
	} {
		if err := s.UpsertProducts(t.Context(), source, products); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	h := NewWithAuth(registry, s, nil, auth, time.Hour)
	call := func(owner, body string) (downloadTaskView, bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/download-tasks", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+keys[owner])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response struct {
			Task    downloadTaskView `json:"task"`
			Created bool             `json:"created"`
		}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("%s: %d %s", owner, w.Code, w.Body.String())
		}
		return response.Task, response.Created
	}
	base := `{"products":["modis:daily","goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","options":{"goes18:ABI-L1b-RadF":{"times":["12:00","00:00","12:00"]}}}`
	equivalent := `{"products":["goes18:ABI-L1b-RadF","modis:daily"],"start":"2024-01-01T08:00:00+08:00","end":"2024-01-02T08:00:00+08:00","options":{"goes18:ABI-L1b-RadF":{"times":["00:00","12:00"]}}}`
	a, created := call("alice", base)
	if !created {
		t.Fatal("first task not created")
	}
	a2, created := call("alice", equivalent)
	if created || a2.ID != a.ID {
		t.Fatalf("equivalent task not deduplicated: %+v", a2)
	}
	b, created := call("bob", base)
	if !created || b.ID == a.ID {
		t.Fatalf("owners incorrectly share task: %+v", b)
	}
	b2, created := call("bob", equivalent)
	if created || b2.ID != b.ID {
		t.Fatalf("bob duplicate: %+v", b2)
	}
	changed, created := call("alice", strings.Replace(equivalent, `"12:00"`, `"13:00"`, 1))
	if !created || changed.ID == a.ID {
		t.Fatalf("changed selection reused: %+v", changed)
	}
	for _, tc := range []struct {
		owner string
		id    int64
		want  int
	}{{"alice", a.ID, 200}, {"bob", a.ID, 404}, {"bob", b.ID, 200}, {"alice", b.ID, 404}} {
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/downloads/batch/%d", tc.id), nil)
		r.Header.Set("Authorization", "Bearer "+keys[tc.owner])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("owner %s id %d: got %d want %d", tc.owner, tc.id, w.Code, tc.want)
		}
	}
}
