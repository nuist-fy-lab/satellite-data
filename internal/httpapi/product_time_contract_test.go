package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestProductTimeTaskContractAndPersistedSummary(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	products := map[string][]model.Product{}
	for _, p := range []model.Product{
		{ProductKey: "calipso:C3880523239-LARC_CLOUD", SourceID: "calipso", AreaType: "ORBIT"},
		{ProductKey: "cloudsat:3F-RMCP.R05_V0002", SourceID: "cloudsat", AreaType: "ORBIT"},
		{ProductKey: "goes18:ABI-Flood-Day", SourceID: "goes18"},
		{ProductKey: "goes18:ABI-Flood-Hourly", SourceID: "goes18"},
		{ProductKey: "modis:C3348093425-LAADS", SourceID: "modis"},
		{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18"},
	} {
		products[p.SourceID] = append(products[p.SourceID], p)
	}
	for source, list := range products {
		if err := store.UpsertProducts(t.Context(), source, list); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(registry, store, nil)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"monthly partial", `{"products":["calipso:C3880523239-LARC_CLOUD"],"start":"2024-01-02","end":"2024-02-01"}`, 400},
		{"daily partial", `{"products":["goes18:ABI-Flood-Day"],"start":"2024-01-01T01:00:00Z","end":"2024-01-02"}`, 400},
		{"hourly forced times", `{"products":["goes18:ABI-Flood-Hourly"],"start":"2024-01-01","end":"2024-01-02","times":["00:00"]}`, 400},
		{"half-hour forced times", `{"products":["modis:C3348093425-LAADS"],"start":"2024-01-01","end":"2024-01-02","times":["00:30"]}`, 400},
		{"scan impossible scene", `{"products":["goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","scenes":{"2024-01-01":["00:03"]}}`, 400},
		{"scan historical slot", `{"products":["goes18:ABI-L1b-RadF"],"start":"2024-01-01","end":"2024-01-02","times":["00:05"]}`, 202},
		{"sparse observation", `{"products":["goes18:ABI-Flood-Hourly"],"start":"2024-01-01","end":"2024-01-02","scenes":{"2024-01-01":["00:03"]}}`, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call("POST", "/api/v1/download-tasks", tc.body)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	body := `{"products":["calipso:C3880523239-LARC_CLOUD","cloudsat:3F-RMCP.R05_V0002","goes18:ABI-Flood-Day","goes18:ABI-Flood-Hourly","modis:C3348093425-LAADS"],"start":"2024-01-01","end":"2024-02-01"}`
	w := call("POST", "/api/v1/download-tasks", body)
	var submitted struct {
		Task downloadTaskView `json:"task"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &submitted) != nil {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/download-tasks?page=1", "")
	var result struct {
		Tasks []downloadTaskView `json:"tasks"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	want := map[string]struct {
		kind, subtype string
		window        int
	}{
		"calipso:C3880523239-LARC_CLOUD": {"monthly", "monthly", 0},
		"cloudsat:3F-RMCP.R05_V0002":     {"observation", "multi_period", 0},
		"goes18:ABI-Flood-Day":           {"daily", "daily_flood", 1440},
		"goes18:ABI-Flood-Hourly":        {"observation", "hourly_flood", 60},
		"modis:C3348093425-LAADS":        {"observation", "multisensor_half_hour", 30},
	}
	for _, task := range result.Tasks {
		if task.ID != submitted.Task.ID {
			continue
		}
		for _, item := range task.Downloads {
			expected, ok := want[item.CollectionID]
			if !ok || item.TimeProfile.Kind != expected.kind || item.TimeProfile.Subtype != expected.subtype || item.TimeProfile.WindowMinutes != expected.window || item.TimeProfile.SemanticLabel == "" {
				t.Fatalf("summary: %+v", item)
			}
			delete(want, item.CollectionID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing persisted summaries: %v", want)
	}
}
