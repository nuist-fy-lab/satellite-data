package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestIndexWatchStaysOpenWhileIdleAndSendsOnlyChangedDays(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "goes18:GLM-L2-LCFA", SourceID: "goes18"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	var granules []model.Granule
	for _, day := range []int{1, 2} {
		granules = append(granules, model.Granule{ID: time.Date(2023, 6, day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), Start: time.Date(2023, 6, day, 0, 0, 0, 0, time.UTC), Assets: []model.Asset{{Name: time.Date(2023, 6, day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly) + ".nc", Size: 7}}})
	}
	if err := s.SaveOfficialBatch(t.Context(), state.WorkBatch{Product: product.ProductKey, Days: []string{"2023-06-01", "2023-06-02"}}, granules); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(provider.NewRegistry(), s))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/index/stream?product="+product.ProductKey+"&year=2023&watch=1", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream headers %+v", response.Header)
	}
	scanner := bufio.NewScanner(response.Body)
	next := func(want string) json.RawMessage {
		t.Helper()
		var event string
		var data json.RawMessage
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = json.RawMessage(strings.TrimPrefix(line, "data: "))
			}
			if line == "" && event != "" {
				if event == "complete" {
					t.Fatal("idle watch was closed")
				}
				if event == want {
					return data
				}
				event = ""
				data = nil
			}
		}
		t.Fatalf("stream ended before %s: %v", want, scanner.Err())
		return nil
	}
	var archive state.ProductArchiveStatus
	if err := json.Unmarshal(next("archive"), &archive); err != nil || archive.Files != 0 || archive.OfficialDays != 2 {
		t.Fatalf("initial archive %+v: %v", archive, err)
	}
	var initial struct {
		Days   []state.YearDay `json:"days"`
		Active bool            `json:"active"`
	}
	if err := json.Unmarshal(next("index"), &initial); err != nil || len(initial.Days) != 2 || initial.Active {
		t.Fatalf("initial index %+v: %v", initial, err)
	}
	s.BeginArchiveScan()
	s.ArchiveScanProgress(128)
	if err := json.Unmarshal(next("archive"), &archive); err != nil || archive.Scan.State != "running" || archive.Scan.Observed != 128 {
		t.Fatalf("live scan %+v: %v", archive, err)
	}
	file := state.LocalFile{Path: filepath.Join(t.TempDir(), "2023-06-01.nc"), Filename: "2023-06-01.nc", Size: 7, State: "ok"}
	if _, err := s.ReplaceLocalFiles(t.Context(), []state.LocalFile{file}, "one-file"); err != nil {
		t.Fatal(err)
	}
	s.FinishArchiveScan(1, nil)
	var delta struct {
		Days   []state.YearDay `json:"days"`
		Active bool            `json:"active"`
	}
	if err := json.Unmarshal(next("index"), &delta); err != nil || len(delta.Days) != 1 || delta.Days[0].Day != "2023-06-01" || delta.Days[0].Status != "complete" || delta.Active {
		t.Fatalf("incremental update %+v: %v", delta, err)
	}
	if _, err := s.ReplaceLocalFiles(t.Context(), nil, "removed-file"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(next("index"), &delta); err != nil || len(delta.Days) != 1 || delta.Days[0].Status != "missing" {
		t.Fatalf("removed archive file %+v: %v", delta, err)
	}
}

func TestIndexOneShotRemainsCompatible(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "goes18:GLM-L2-LCFA", SourceID: "goes18"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	New(provider.NewRegistry(), s).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/index/stream?product="+product.ProductKey+"&year=2023", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "event: complete") {
		t.Fatalf("one-shot stream: %s", w.Body.String())
	}
}
