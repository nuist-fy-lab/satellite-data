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
