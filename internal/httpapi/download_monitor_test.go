package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/app"
	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestDownloadMonitorEndpoint(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordDownloadSamples(t.Context(), time.Now().UTC().Add(-time.Second), []downloader.ProviderStatus{{SourceID: "goes", Enabled: true, BytesPerSecond: 2048, ActiveTransfers: 2, AdaptiveConcurrency: 4}}); err != nil {
		t.Fatal(err)
	}
	registry, err := app.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(registry, store)
	for _, tc := range []struct {
		window   string
		status   int
		contains string
	}{
		{"realtime", http.StatusOK, `"source_id":"goes"`},
		{"1w", http.StatusOK, `"sample_interval_seconds":15`},
		{"invalid", http.StatusBadRequest, `unsupported download monitor window`},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/monitor/downloads?window="+tc.window, nil))
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.contains) {
			t.Errorf("window=%s status=%d body=%s", tc.window, response.Code, response.Body.String())
		}
	}
}
