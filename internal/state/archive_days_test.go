package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestGOESArchiveDiscoveryPreservesRetryBackoff(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	days := []ArchiveDay{{Product: product.ProductKey, Day: "2026-09-22", Fingerprint: "local-v1", Filenames: []string{"file.nc"}}}
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), days); err != nil || q != 1 || r != 1 {
		t.Fatalf("first discovery queued=%d recounted=%d err=%v", q, r, err)
	}
	if _, err := s.writer.Exec(`UPDATE official_work SET failure_count=3,next_attempt_ns=12345,state='running'`); err != nil {
		t.Fatal(err)
	}
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), days); err != nil || q != 0 || r != 0 {
		t.Fatalf("repeat scan queued=%d recounted=%d err=%v", q, r, err)
	}
	var failures int
	var next int64
	var workState string
	if err := s.db.QueryRow(`SELECT failure_count,next_attempt_ns,state FROM official_work`).Scan(&failures, &next, &workState); err != nil {
		t.Fatal(err)
	}
	if failures != 3 || next != 12345 || workState != "running" {
		t.Fatalf("scan reset lookup state: %d %d %s", failures, next, workState)
	}
}

func TestGOESArchiveRecountsPersistedSummaryAndSkipsUnchangedDates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "goes18:GLM-L2-LCFA", SourceID: "goes18", Cadence: "event"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 22, 9, 30, 0, 0, time.UTC)
	day := start.Format(time.DateOnly)
	granules := []model.Granule{{ID: "slot", Start: start, Assets: []model.Asset{{Name: "file.nc", Size: 10}}}}
	if err := s.SaveOfficialBatch(t.Context(), WorkBatch{Product: product.ProductKey, Days: []string{day}}, granules); err != nil {
		t.Fatal(err)
	}
	files := []LocalFile{{Path: "/archive/file.nc", Filename: "file.nc", Size: 10, State: "ok"}}
	if _, err := s.ReplaceLocalFiles(t.Context(), files, "files-v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`UPDATE day_summary SET local_ok_count=0`); err != nil {
		t.Fatal(err)
	}
	days := []ArchiveDay{{Product: product.ProductKey, Day: day, Fingerprint: "local-v1", Filenames: []string{"file.nc"}}}
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), days); err != nil || q != 0 || r != 1 {
		t.Fatalf("repair summary queued=%d recounted=%d err=%v", q, r, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT local_ok_count FROM day_summary`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("summary count=%d err=%v", count, err)
	}
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), days); err != nil || q != 0 || r != 0 {
		t.Fatalf("unchanged queued=%d recounted=%d err=%v", q, r, err)
	}
	if _, err := s.ReplaceLocalFiles(t.Context(), nil, "removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT local_ok_count FROM day_summary`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed file still complete count=%d err=%v", count, err)
	}
}

func TestGOESArchiveMissingAssetRefreshIsBoundedAndCatalogueRevisionRecounts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := ArchiveDay{Product: product.ProductKey, Day: "2026-09-22", Fingerprint: "v1", Filenames: []string{"new.nc"}}
	if err := s.SaveOfficialBatch(t.Context(), WorkBatch{Product: day.Product, Days: []string{day.Day}}, nil); err != nil {
		t.Fatal(err)
	}
	if q, _, err := s.ReconcileGOESArchiveDays(t.Context(), []ArchiveDay{day}); err != nil || q != 0 {
		t.Fatalf("fresh missing asset triggered query: q=%d err=%v", q, err)
	}
	stale := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339Nano)
	if _, err := s.writer.Exec(`UPDATE official_days SET refreshed_at=?`, stale); err != nil {
		t.Fatal(err)
	}
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), []ArchiveDay{day}); err != nil || q != 1 || r != 1 {
		t.Fatalf("stale catalogue: q=%d r=%d err=%v", q, r, err)
	}
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), []ArchiveDay{day}); err != nil || q != 0 || r != 0 {
		t.Fatalf("duplicate refresh: q=%d r=%d err=%v", q, r, err)
	}
	day.Product = "not-registered"
	if q, r, err := s.ReconcileGOESArchiveDays(t.Context(), []ArchiveDay{day}); err != nil || q != 0 || r != 0 {
		t.Fatalf("unknown product queued: q=%d r=%d err=%v", q, r, err)
	}
}
