package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestArchiveStatusScanLifecycleAndProductYears(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, err := s.ArchiveScanStatus(t.Context())
	if err != nil || status.State != "pending" {
		t.Fatalf("empty scan %+v: %v", status, err)
	}
	s.BeginArchiveScan()
	s.ArchiveScanProgress(128)
	status, err = s.ArchiveScanStatus(t.Context())
	if err != nil || status.State != "running" || status.Observed != 128 || status.StartedAt == "" {
		t.Fatalf("running scan %+v: %v", status, err)
	}
	product := model.Product{ProductKey: "goes18:GLM-L2-LCFA", SourceID: "goes18"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	var files []LocalFile
	for index, year := range []int{2023, 2025} {
		day := time.Date(year, 6, 1, 12, 0, 0, 0, time.UTC)
		name := day.Format("2006") + ".nc"
		if err := s.SaveOfficialBatch(t.Context(), WorkBatch{Product: product.ProductKey, Days: []string{day.Format(time.DateOnly)}}, []model.Granule{{ID: name, Start: day, Assets: []model.Asset{{Name: name, Size: 7}}}}); err != nil {
			t.Fatal(err)
		}
		files = append(files, LocalFile{Filename: name, Path: filepath.Join(t.TempDir(), name), Size: 7, State: "ok"})
		if _, err := s.ReplaceLocalFiles(t.Context(), files, name); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			s.FinishArchiveScan(128, nil)
		}
	}
	result, err := s.ProductArchiveIndexStatus(t.Context(), product.ProductKey, 2025)
	if err != nil || result.Files != 2 || result.YearFiles != 1 || len(result.Years) != 2 || result.FirstDay != "2023-06-01" || result.LastDay != "2025-06-01" || result.OfficialDays != 1 || result.Scan.State != "ready" {
		t.Fatalf("product status %+v: %v", result, err)
	}
	completed := result.Scan.ScannedAt
	s.BeginArchiveScan()
	s.FinishArchiveScan(0, errors.New("fixture scan failure"))
	status, err = s.ArchiveScanStatus(t.Context())
	if err != nil || status.State != "error" || status.LastError != "fixture scan failure" || status.ScannedAt != completed {
		t.Fatalf("failed scan overwrote success %+v: %v", status, err)
	}
	s.BeginArchiveScan()
	s.FinishArchiveScan(2, nil)
	status, err = s.ArchiveScanStatus(t.Context())
	if err != nil || status.State != "ready" || status.LastError != "" || status.Observed != 2 || status.ScannedAt == completed {
		t.Fatalf("unchanged scan freshness %+v: %v", status, err)
	}
}
