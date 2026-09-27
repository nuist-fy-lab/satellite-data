package era5

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

func TestCatalogueUsesOfficialMetadataAndCachesWithoutAlias(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("catalogue must be anonymous")
		}
		id := strings.TrimPrefix(r.URL.Path, "/catalogue/v1/collections/")
		fmt.Fprintf(w, `{"id":%q,"title":%q,"license":"CC-BY-4.0","extent":{"temporal":{"interval":[["1940-01-01T00:00:00Z","2026-09-21T00:00:00Z"]]}}}`, id, "Official "+id)
	}))
	defer server.Close()
	p := New(server.Client())
	p.base = server.URL
	collections, err := p.Collections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(collections) != 6 || calls.Load() != 6 {
		t.Fatalf("count=%d calls=%d", len(collections), calls.Load())
	}
	for n, collection := range collections {
		if collection.ID != "era5:"+datasets[n].id || collection.Metadata["title"] != "Official "+datasets[n].id || collection.Metadata["temporal_end"] != "2026-09-21T00:00:00Z" {
			t.Fatalf("wrong collection: %#v", collection)
		}
		if collection.Metadata["availability_mode"] != "on-demand" || collection.SourceID != "era5" {
			t.Fatal("missing on-demand metadata")
		}
		product := provider.ProductFromCollection(p.Source(), collection)
		if product.CatalogSource != "Copernicus Climate Data Store STAC catalogue" || product.OfficialID != datasets[n].id {
			t.Fatalf("product=%#v", product)
		}
	}
	collections[0].Metadata["title"] = "mutated"
	again, err := p.Collections(t.Context())
	if err != nil || again[0].Metadata["title"] == "mutated" || calls.Load() != 6 {
		t.Fatalf("cache alias or missed cache: err=%v calls=%d", err, calls.Load())
	}
}

func TestCatalogueDoesNotHidePartialFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "reanalysis-era5-land") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/catalogue/v1/collections/")
		fmt.Fprintf(w, `{"id":%q,"title":"Official"}`, id)
	}))
	defer server.Close()
	p := New(server.Client())
	p.base = server.URL
	if collections, err := p.Collections(t.Context()); err == nil || len(collections) != 0 || len(p.catalog) != 0 {
		t.Fatal("partial catalogue reported as complete")
	}
}

func TestSearchNeverSubmitsOrInventsGranules(t *testing.T) {
	p := New(nil)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	q := model.Query{CollectionID: "era5:reanalysis-era5-single-levels", Start: start, End: start.Add(time.Hour)}
	granules, err := p.Search(t.Context(), q)
	if !errors.Is(err, provider.ErrOnDemand) || len(granules) != 0 {
		t.Fatalf("granules=%v error=%v", granules, err)
	}
	q.CollectionID = "era5:unknown"
	if _, err := p.Search(t.Context(), q); !errors.Is(err, provider.ErrUnsupportedCollection) {
		t.Fatal(err)
	}
	for _, capability := range p.Source().Capabilities {
		if capability == model.CapabilitySearch || capability == model.CapabilityDownload || capability == model.CapabilitySubscription {
			t.Fatal("advertised an unsupported capability")
		}
	}
}

func TestAsynchronousSubmissionJobAndResult(t *testing.T) {
	var submissions atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retrieve/v1/processes/reanalysis-era5-pressure-levels":
			if r.Header.Get("PRIVATE-TOKEN") != "" {
				t.Error("public schema leaked token")
			}
			fmt.Fprint(w, `{"id":"reanalysis-era5-pressure-levels","inputs":{"variable":{},"pressure_level":{},"year":{}}}`)
		case "/retrieve/v1/processes/reanalysis-era5-pressure-levels/execution":
			submissions.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("PRIVATE-TOKEN") != "test-only-placeholder" {
				t.Error("invalid authenticated execution request")
			}
			var payload map[string]map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload) != 1 || len(payload["inputs"]) != 3 {
				t.Error("missing CDS inputs envelope")
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"jobID":"request-1","processID":"reanalysis-era5-pressure-levels","status":"accepted","metadata":{"request":{"private":"do not return"}}}`)
		case "/retrieve/v1/jobs/request-1":
			if r.Header.Get("PRIVATE-TOKEN") != "test-only-placeholder" {
				t.Error("unauthenticated job read")
			}
			fmt.Fprint(w, `{"jobID":"request-1","status":"successful"}`)
		case "/retrieve/v1/jobs/request-1/results":
			if r.Header.Get("PRIVATE-TOKEN") != "test-only-placeholder" {
				t.Error("unauthenticated result read")
			}
			fmt.Fprint(w, `{"asset":{"value":{"href":"https://object-store.os-api.cci2.ecmwf.int/cache/result.grib","file:size":2048,"type":"application/x-grib","file:local_path":"/private/result"}}}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	p := New(server.Client())
	p.base = server.URL
	p.token = func() (string, error) { return "test-only-placeholder", nil }
	job, err := p.Submit(t.Context(), Submission{CollectionID: "era5:reanalysis-era5-pressure-levels", Inputs: map[string]json.RawMessage{
		"variable": json.RawMessage(`["temperature"]`), "pressure_level": json.RawMessage(`["850"]`), "year": json.RawMessage(`["2025"]`),
	}})
	if err != nil || job.ID != "request-1" || job.Status != "accepted" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	encoded, _ := json.Marshal(job)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("job metadata leaked")
	}
	job, err = p.Job(t.Context(), job.ID)
	if err != nil || job.Status != "successful" {
		t.Fatalf("job=%#v error=%v", job, err)
	}
	asset, err := p.Result(t.Context(), job.ID)
	if err != nil || asset.Name != "result.grib" || asset.Size != 2048 || asset.AuthRef != "" {
		t.Fatalf("asset=%#v error=%v", asset, err)
	}
	if submissions.Load() != 1 {
		t.Fatal("polling resubmitted a job")
	}
	if _, err := p.Job(t.Context(), "../other"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("accepted traversal job ID")
	}
	if _, err := p.Submit(t.Context(), Submission{CollectionID: "era5:reanalysis-era5-pressure-levels", Inputs: map[string]json.RawMessage{"secret": json.RawMessage(`"test"`)}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("accepted unsupported fields")
	}
	if submissions.Load() != 1 {
		t.Fatal("invalid input submitted")
	}
}

func TestProtectedRequestsDoNotFollowRedirectsOrEchoErrors(t *testing.T) {
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "redirect") {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"detail":"test-only-placeholder"}`)
	}))
	defer server.Close()
	p := New(server.Client())
	p.base = server.URL
	p.token = func() (string, error) { return "test-only-placeholder", nil }
	for _, id := range []string{"redirect", "denied"} {
		_, err := p.Job(t.Context(), id)
		var response *HTTPError
		if !errors.As(err, &response) || strings.Contains(err.Error(), "test-only-placeholder") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("followed a protected redirect")
	}
}

func TestTokenFileRequiresPrivatePermissions(t *testing.T) {
	t.Setenv("CDS_API_TOKEN_FILE", "")
	if _, err := tokenFromFile(); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatal("missing token accepted")
	}
	filename := filepath.Join(t.TempDir(), "cds-token")
	if err := os.WriteFile(filename, []byte("test-only-placeholder\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CDS_API_TOKEN_FILE", filename)
	if token, err := tokenFromFile(); err != nil || token != "test-only-placeholder" {
		t.Fatal("private token file rejected")
	}
	if err := os.Chmod(filename, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := tokenFromFile(); err == nil {
		t.Fatal("world-readable token file accepted")
	}
	if err := os.Chmod(filename, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("invalid\r\nheader"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tokenFromFile(); err == nil {
		t.Fatal("multi-line token accepted")
	}
}
