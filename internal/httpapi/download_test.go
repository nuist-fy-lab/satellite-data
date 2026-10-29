package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestSelectedDownloadCatalogAndSubmission(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const collection = "goes18:ABI-L1b-RadF"
	if err := store.UpsertProducts(t.Context(), "goes18", []model.Product{{ProductKey: collection, SourceID: "goes18", DataType: "ABI-L1b-RadF", AreaType: "DISK"}}); err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(registry, store)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/downloads/catalog?source=goes18", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("catalog %d: %s", get.Code, get.Body.String())
	}
	var catalog struct {
		Products []struct {
			model.Product
			Bands []struct {
				Number int `json:"number"`
			} `json:"bands"`
		} `json:"products"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Products) != 1 || len(catalog.Products[0].Bands) != 16 || catalog.Products[0].Bands[5].Number != 6 {
		t.Fatalf("catalog=%+v", catalog)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"source":"goes18","collection":"goes18:ABI-L1b-RadF","start":"2023-06-01","end":"2023-07-01","band":6,"times":["01:00","00:00"]}`, http.StatusAccepted},
		{`{"source":"goes18","collection":"goes18:ABI-L1b-RadF","start":"2023-06-01","end":"2023-07-01","band":17}`, http.StatusBadRequest},
		{`{"source":"goes18","collection":"goes18:ABI-L1b-RadF","start":"2023-06-01","end":"2023-07-01","times":["24:00"]}`, http.StatusBadRequest},
	} {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/v1/downloads", strings.NewReader(tc.body)))
		if resp.Code != tc.status {
			t.Errorf("status=%d want=%d body=%s", resp.Code, tc.status, resp.Body.String())
		}
	}
	requests, err := store.DownloadRequests(t.Context(), 10)
	if err != nil || len(requests) != 1 || requests[0].Selection.Band != 6 || requests[0].Selection.Times[0] != "00:00" {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
}

func TestSingleProductGOESSparseSceneSubmission(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const collection = "goes18:ABI-L1b-RadC"
	if err := store.UpsertProducts(t.Context(), "goes18", []model.Product{{
		ProductKey: collection, SourceID: "goes18", DataType: "ABI-L1b-RadC", AreaType: "CONUS",
	}}); err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(registry, store)

	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{"CLI null scenes", `{"source":"goes18","collection":"goes18:ABI-L1b-RadC","start":"2025-06-07","end":"2025-06-08","band":3,"times":["18:00"],"scenes":null,"inputs":{}}`, http.StatusAccepted},
		{"sparse scenes", `{"source":"goes18","collection":"goes18:ABI-L1b-RadC","start":"2025-06-07","end":"2025-06-09","band":7,"scenes":{"2025-06-07":["18:00","18:00"],"2025-06-08":["00:00"]},"inputs":{}}`, http.StatusAccepted},
		{"out of range scene", `{"source":"goes18","collection":"goes18:ABI-L1b-RadC","start":"2025-06-07","end":"2025-06-09","band":7,"scenes":{"2025-06-09":["00:00"]},"inputs":{}}`, http.StatusBadRequest},
		{"conflicting time selection", `{"source":"goes18","collection":"goes18:ABI-L1b-RadC","start":"2025-06-07","end":"2025-06-09","band":7,"times":["18:00"],"scenes":{"2025-06-07":["18:00"]},"inputs":{}}`, http.StatusBadRequest},
		{"invalid scene minute", `{"source":"goes18","collection":"goes18:ABI-L1b-RadC","start":"2025-06-07","end":"2025-06-09","band":7,"scenes":{"2025-06-07":["24:00"]},"inputs":{}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/v1/downloads", strings.NewReader(tc.body)))
			if resp.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", resp.Code, tc.status, resp.Body.String())
			}
		})
	}
	requests, err := store.DownloadRequests(t.Context(), 10)
	if err != nil || len(requests) != 2 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	if requests[0].Selection.Band != 7 || len(requests[0].Selection.Scenes) != 2 ||
		len(requests[0].Selection.Scenes["2025-06-07"]) != 1 ||
		requests[0].Selection.Scenes["2025-06-07"][0] != "18:00" ||
		requests[1].Selection.Band != 3 || len(requests[1].Selection.Times) != 1 {
		t.Fatalf("incorrect persisted selections: %+v", requests)
	}
}

func TestDownloadHistoryHTTPPaginatesInBatchesOfFifty(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(registry, store)
	start, _ := time.Parse(time.DateOnly, "2023-01-01")
	for i := 0; i < 53; i++ {
		if _, err := store.EnqueueDownloadRequest(t.Context(), "modis", "modis", "modis:sample", start, start.AddDate(0, 0, 1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, testCase := range []struct {
		page                      string
		code, count, total, pages int
	}{
		{"1", 200, 50, 53, 2}, {"2", 200, 3, 53, 2}, {"3", 200, 0, 53, 2}, {"0", 400, 0, 0, 0}, {"xyz", 400, 0, 0, 0},
	} {
		t.Run(testCase.page, func(t *testing.T) {
			r := httptest.NewRecorder()
			handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/downloads?page="+testCase.page+"&limit=500", nil))
			if r.Code != testCase.code {
				t.Fatalf("HTTP %d expected %d: %s", r.Code, testCase.code, r.Body.String())
			}
			if r.Code != 200 {
				return
			}
			var response struct {
				Downloads []downloadSummary `json:"downloads"`
				Page      int               `json:"page"`
				PageSize  int               `json:"page_size"`
				Total     int               `json:"total"`
				Pages     int               `json:"pages"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Downloads) != testCase.count || response.Total != testCase.total || response.Pages != testCase.pages || response.PageSize != 50 {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}
