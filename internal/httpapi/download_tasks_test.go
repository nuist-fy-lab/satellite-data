package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/authstore"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestDownloadTasksSubmitOnceSummaryPagingAndOwnership(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := authstore.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	keys := make(map[string]string)
	for _, name := range []string{"alice", "bob"} {
		user, err := auth.CreateUser(t.Context(), name, "test-password-1234", "user")
		if err != nil {
			t.Fatal(err)
		}
		_, key, err := auth.CreateAPIKey(t.Context(), user.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = key
	}
	for _, product := range []model.Product{
		{ProductKey: "modis:sample", SourceID: "modis", Satellite: "Terra", Instrument: "MODIS", DataType: "MOD06_L2", DisplayName: "Cloud product"},
		{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Satellite: "GOES-18", Instrument: "ABI", DataType: "ABI-L1b-RadF", DisplayName: "Full disk radiance"},
	} {
		if err := store.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithAuth(registry, store, nil, auth, time.Hour)
	call := func(method, path, body, owner string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+keys[owner])
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"products":["modis:sample","goes18:ABI-L1b-RadF"],"start":"2026-01-01","end":"2026-01-03","times":["00:00","12:00"]}`
	var submitted struct {
		Task    downloadTaskView `json:"task"`
		Created bool             `json:"created"`
	}
	first := call("POST", "/api/v1/download-tasks", body, "alice")
	if first.Code != http.StatusAccepted || json.Unmarshal(first.Body.Bytes(), &submitted) != nil {
		t.Fatalf("submit: %d %s", first.Code, first.Body.String())
	}
	if !submitted.Created || submitted.Task.Products != 2 || len(submitted.Task.Downloads) != 2 {
		t.Fatalf("task: %+v", submitted)
	}
	for _, item := range submitted.Task.Downloads {
		if item.Satellite == "" || item.ProductName == "" || item.ProductCode == "" || len(item.Selection.Times) != 2 {
			t.Fatalf("missing summary: %+v", item)
		}
	}
	duplicate := call("POST", "/api/v1/download-tasks", body, "alice")
	var again struct {
		Task    downloadTaskView `json:"task"`
		Created bool             `json:"created"`
	}
	if duplicate.Code != http.StatusAccepted || json.Unmarshal(duplicate.Body.Bytes(), &again) != nil || again.Created || again.Task.ID != submitted.Task.ID {
		t.Fatalf("duplicate: %d %s", duplicate.Code, duplicate.Body.String())
	}
	// Unassociated legacy work is not surfaced as a second task/history list.
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.EnqueueDownloadRequest(t.Context(), "modis", "modis", "modis:sample", start, start.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 31; i++ {
		body := fmt.Sprintf(`{"products":["modis:sample"],"start":"2026-02-%02d","end":"2026-04-01"}`, i%28+1)
		// Distinct windows, including the final three extending into March.
		if i >= 28 {
			body = fmt.Sprintf(`{"products":["modis:sample"],"start":"2026-03-%02d","end":"2026-04-01"}`, i-27)
		}
		got := call("POST", "/api/v1/download-tasks", body, "alice")
		if got.Code != http.StatusAccepted {
			t.Fatalf("extra task: %d %s", got.Code, got.Body.String())
		}
	}
	for _, tc := range []struct {
		owner, page  string
		count, total int
	}{{"alice", "1", 30, 32}, {"alice", "2", 2, 32}, {"bob", "1", 0, 0}} {
		got := call("GET", "/api/v1/download-tasks?page="+tc.page+"&limit=500", "", tc.owner)
		var result struct {
			Tasks []downloadTaskView `json:"tasks"`
			Total int                `json:"total"`
			Size  int                `json:"page_size"`
		}
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &result) != nil || len(result.Tasks) != tc.count || result.Total != tc.total || result.Size != 30 {
			t.Fatalf("page %s/%s: %d %s", tc.owner, tc.page, got.Code, got.Body.String())
		}
	}
	invalid := call("POST", "/api/v1/download-tasks", `{"products":["modis:sample","missing:product"],"start":"2026-01-01","end":"2026-01-02"}`, "alice")
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("invalid selection: %d", invalid.Code)
	}
	if got := call("GET", "/api/v1/download-tasks?page=0", "", "alice"); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid page: %d", got.Code)
	}
}
