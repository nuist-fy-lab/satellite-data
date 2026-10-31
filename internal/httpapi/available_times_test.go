package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type availableTestProvider struct {
	source   string
	granules []model.Granule
	entered  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
	err      error
}

func TestAvailableGranuleWhitelist(t *testing.T) {
	for _, key := range []string{"goes18:ABI-L1b-RadF", "goes18:ABI-L2-CMIPC", "goes18:ABI-L2-ACHAF", "goes18:ABI-L2-ACMF"} {
		if !availableGranuleProduct("goes18", key) {
			t.Fatalf("missing %s", key)
		}
	}
	for _, key := range []string{"goes18:ABI-L2-UNKNOWNF", "goes18:ABI-L2-ACHAC", "goes18:ABI-L2-ACMC", "goes18:ABI-L1b-RadM", "goes18:ABI-L2-CMIPM", "goes19:ABI-L2-CMIPF", "himawari9:AHI-L1b-Japan"} {
		if availableGranuleProduct("goes18", key) {
			t.Fatalf("unexpected %s", key)
		}
	}
}

func (p *availableTestProvider) Source() model.Source {
	if p.source != "" {
		return model.Source{ID: p.source}
	}
	return model.Source{ID: "viirs"}
}
func (*availableTestProvider) Collections(context.Context) ([]model.Collection, error) {
	return nil, nil
}
func (p *availableTestProvider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	p.calls.Add(1)
	if p.entered != nil {
		p.entered <- struct{}{}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.granules, p.err
}

func TestDerivedGOESGranuleListingAndSubmission(t *testing.T) {
	for _, code := range []string{"ABI-L2-ACHAF", "ABI-L2-ACMF"} {
		t.Run(code, func(t *testing.T) {
			store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			key := "goes18:" + code
			if err := store.UpsertProducts(t.Context(), "goes18", []model.Product{{SourceID: "goes18", ProductKey: key, Cadence: "10m"}}); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2024, 1, 1, 0, 0, 20, 900000000, time.UTC)
			p := &availableTestProvider{source: "goes18", granules: []model.Granule{{ID: "scene", Start: start, End: start.Add(time.Minute)}}}
			h := New(provider.NewRegistry(p), store)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/downloads/available-times?product="+key+"&date=2024-01-01", nil))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "00:00:20.9Z") {
				t.Fatalf("listing %d %s", w.Code, w.Body.String())
			}
			body := `{"products":["` + key + `"],"start":"2024-01-01","end":"2024-01-02","options":{"` + key + `":{"granule_ids":["scene"]}}}`
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/download-tasks", strings.NewReader(body)))
			var result struct {
				Task downloadTaskView `json:"task"`
			}
			if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Task.Downloads) != 1 || result.Task.Downloads[0].TimeProfile.Kind != "observation" || len(result.Task.Downloads[0].Selection.GranuleIDs) != 1 {
				t.Fatalf("task %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAvailableGranulesBusy(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "viirs:C3860041042-LAADS"
	if err := store.UpsertProducts(t.Context(), "viirs", []model.Product{{SourceID: "viirs", ProductKey: key}}); err != nil {
		t.Fatal(err)
	}
	p := &availableTestProvider{entered: make(chan struct{}, 2), release: make(chan struct{})}
	h := New(provider.NewRegistry(p), store)
	protected := NewWithAuth(provider.NewRegistry(p), store, nil, nil, time.Hour)
	unauth := httptest.NewRecorder()
	protected.ServeHTTP(unauth, httptest.NewRequest("GET", "/api/v1/downloads/available-times?product="+key+"&date=2024-01-01", nil))
	if unauth.Code != http.StatusUnauthorized || p.calls.Load() != 0 {
		t.Fatalf("unauthorized status %d calls %d", unauth.Code, p.calls.Load())
	}
	path := "/api/v1/downloads/available-times?product=" + key + "&date=2024-01-01"
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil)); done <- struct{}{} }()
	}
	<-p.entered
	<-p.entered
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("busy status %d", w.Code)
	}
	close(p.release)
	<-done
	<-done
}

func TestAvailableGranulesAndTaskIdentity(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "viirs:C3860041042-LAADS"
	if err := store.UpsertProducts(t.Context(), "viirs", []model.Product{{SourceID: "viirs", ProductKey: key}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 2, 30, 123000000, time.UTC)
	p := &availableTestProvider{granules: []model.Granule{{ID: "viirs:g1", Start: start}, {ID: "viirs:g2", Start: start, End: start.Add(time.Minute)}}}
	h := New(provider.NewRegistry(p), store)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	w := get("/api/v1/downloads/available-times?product=" + key + "&date=2024-01-01")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "00:02:30.123Z") || strings.Contains(w.Body.String(), "0001-01-01") {
		t.Fatalf("listing %d %s", w.Code, w.Body.String())
	}
	if w = get("/api/v1/downloads/available-times?product=" + key + "&date=2024-02-30"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid date %d", w.Code)
	}
	if w = get("/api/v1/downloads/available-times?product=viirs:unknown&date=2024-01-01"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown product %d", w.Code)
	}
	if p.calls.Load() != 1 {
		t.Fatal("invalid requests reached provider")
	}
	p.err = errors.New("partial")
	if w = get("/api/v1/downloads/available-times?product=" + key + "&date=2024-01-01"); w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "viirs:g1") {
		t.Fatalf("provider error leaked partial: %d %s", w.Code, w.Body.String())
	}
	p.err = nil
	p.granules = make([]model.Granule, 2001)
	if w = get("/api/v1/downloads/available-times?product=" + key + "&date=2024-01-01"); w.Code != http.StatusBadGateway {
		t.Fatalf("oversized result %d", w.Code)
	}
	body := `{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02","options":{"viirs:C3860041042-LAADS":{"granule_ids":["viirs:g2","viirs:g1","viirs:g1"]}}}`
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/download-tasks", strings.NewReader(body)))
	var result struct {
		Task    downloadTaskView `json:"task"`
		Created bool             `json:"created"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Task.Downloads) != 1 || len(result.Task.Downloads[0].Selection.GranuleIDs) != 2 || result.Task.Downloads[0].Selection.GranuleIDs[0] != "viirs:g1" {
		t.Fatalf("task %d %s", w.Code, w.Body.String())
	}
	first := result.Task.ID
	for _, tc := range []struct {
		ids     string
		created bool
	}{{`["viirs:g1","viirs:g2"]`, false}, {`["viirs:g1"]`, true}} {
		request := `{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02","options":{"viirs:C3860041042-LAADS":{"granule_ids":` + tc.ids + `}}}`
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/download-tasks", strings.NewReader(request)))
		result = struct {
			Task    downloadTaskView `json:"task"`
			Created bool             `json:"created"`
		}{}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Created != tc.created || (result.Task.ID == first) == tc.created {
			t.Fatalf("dedup %s: %d %s", tc.ids, w.Code, w.Body.String())
		}
	}
	for _, invalid := range []string{"[]", "null"} {
		request := `{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02","options":{"viirs:C3860041042-LAADS":{"granule_ids":` + invalid + `}}}`
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/download-tasks", strings.NewReader(request)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("empty selection %s: %d %s", invalid, w.Code, w.Body.String())
		}
		request = `{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02","granule_ids":` + invalid + `}`
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/download-tasks", strings.NewReader(request)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("empty shared %s: %d %s", invalid, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02"}`, http.StatusAccepted},
		{`{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02","granule_ids":["a"],"options":{"viirs:C3860041042-LAADS":{"granule_ids":["b"]}}}`, http.StatusAccepted},
		{`{"products":["viirs:C3860041042-LAADS"],"start":"2024-01-01","end":"2024-01-02","granule_ids":["a"],"scenes":{"2024-01-01":["00:00"]}}`, http.StatusBadRequest},
	} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/download-tasks", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("request: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(tc.body, `["b"]`) && !strings.Contains(w.Body.String(), `"granule_ids":["b"]`) {
			t.Fatalf("override: %s", w.Body.String())
		}
	}
}
