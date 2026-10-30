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

func TestIndependentProductTimeBandAndVersionAreAtomicAndDeduplicated(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for source, products := range map[string][]model.Product{
		"modis": {{ProductKey: "modis:day", SourceID: "modis", Cadence: "1d", Version: "61"},
			{ProductKey: "modis:month", SourceID: "modis", Cadence: "1mo", Version: "6"},
			{ProductKey: "modis:swath", SourceID: "modis", Cadence: "5m"}},
		"goes18":    {{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}},
		"himawari9": {{ProductKey: "himawari9:AHI-L1b-FLDK", SourceID: "himawari9", Cadence: "10m"}},
	} {
		if err := store.UpsertProducts(t.Context(), source, products); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(registry, store)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/download-tasks", strings.NewReader(body)))
		return w
	}
	body := `{"products":["modis:day","modis:month","goes18:ABI-L1b-RadF","himawari9:AHI-L1b-FLDK"],"start":"2024-01-01","end":"2024-04-01","options":{
	 "modis:day":{"start":"2024-02-29","end":"2024-03-01","times":[]},
	 "modis:month":{"start":"2024-02-01","end":"2024-03-01"},
	 "goes18:ABI-L1b-RadF":{"start":"2024-01-01T16:00:00Z","end":"2024-01-02T16:00:00Z","times":["00:00","12:00"],"band":6},
	 "himawari9:AHI-L1b-FLDK":{"start":"2024-01-03","end":"2024-01-04","times":["01:00"],"band":3}}}`
	first := call(body)
	var got struct {
		Created bool             `json:"created"`
		Task    downloadTaskView `json:"task"`
	}
	if first.Code != http.StatusAccepted || json.Unmarshal(first.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", first.Code, first.Body.String())
	}
	if !got.Created || len(got.Task.Downloads) != 4 {
		t.Fatalf("%+v", got)
	}
	for _, item := range got.Task.Downloads {
		switch item.CollectionID {
		case "modis:day":
			if item.Start.Format("2006-01-02") != "2024-02-29" || item.End.Format("2006-01-02") != "2024-03-01" || item.Version != "61" || len(item.Selection.Times) != 0 {
				t.Fatalf("%+v", item)
			}
		case "modis:month":
			if item.Start.Format("2006-01-02") != "2024-02-01" || item.TimeProfile.Kind != "monthly" {
				t.Fatalf("%+v", item)
			}
		case "goes18:ABI-L1b-RadF":
			if item.Selection.Band != 6 || len(item.Selection.Times) != 2 {
				t.Fatalf("%+v", item)
			}
		case "himawari9:AHI-L1b-FLDK":
			if item.Selection.Band != 3 || len(item.Selection.Times) != 1 {
				t.Fatalf("%+v", item)
			}
		}
	}
	// Outer defaults are not part of identity when every product overrides them.
	duplicate := call(strings.Replace(body, `"end":"2024-04-01"`, `"end":"2024-05-01"`, 1))
	var again struct {
		Created bool             `json:"created"`
		Task    downloadTaskView `json:"task"`
	}
	if duplicate.Code != http.StatusAccepted || json.Unmarshal(duplicate.Body.Bytes(), &again) != nil || again.Created || again.Task.ID != got.Task.ID {
		t.Fatalf("duplicate %d %s", duplicate.Code, duplicate.Body.String())
	}
	for _, bad := range []string{
		strings.Replace(body, `"start":"2024-02-01"`, `"start":"2024-02-02"`, 1),
		strings.Replace(body, `"times":[]`, `"times":["00:00"]`, 1),
		strings.Replace(body, `"times":["00:00","12:00"]`, `"times":["00:03"]`, 1),
		strings.Replace(body, `"band":6`, `"band":99`, 1),
		`{"products":["modis:swath"],"start":"2024-01-01","end":"2024-01-02","times":["00:00"]}`,
		`{"products":["modis:day"],"start":"2024-01-01T00:00:00+08:00","end":"2024-01-02T00:00:00+08:00"}`,
		`{"products":["modis:day"],"start":"2024-01-01","end":"2024-01-02","options":{"modis:month":{}}}`,
	} {
		if rejected := call(bad); rejected.Code != http.StatusBadRequest {
			t.Fatalf("invalid submission: %d %s", rejected.Code, rejected.Body.String())
		}
	}
	if tasks, err := store.DownloadRequests(t.Context(), 100); err != nil || len(tasks) != 4 {
		t.Fatalf("partial/duplicate requests: %d %v", len(tasks), err)
	}
}
