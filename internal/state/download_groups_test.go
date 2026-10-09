package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestBatchOrdersAreAtomicOwnedAndIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	start := time.Date(2023, 5, 3, 0, 0, 0, 0, time.UTC)
	specs := []GroupProduct{
		{Source: "modis", Provider: "modis", Collection: "modis:C1", Selection: downloadfilter.Selection{}},
		{Source: "goes18", Provider: "goes", Collection: "goes18:ABI-L1b-RadF", Selection: downloadfilter.Selection{}},
	}
	group, created, err := s.EnqueueDownloadGroup(ctx, "user-1", start, start.AddDate(0, 0, 2), specs)
	if err != nil || !created || group.Products != 2 || group.ID <= 0 || group.State != "running" {
		t.Fatalf("invalid batch: %+v created=%v err=%v", group, created, err)
	}
	// Reversing products is the same logical batch; no children are created.
	groupAgain, created, err := s.EnqueueDownloadGroup(ctx, "user-1", start, start.AddDate(0, 0, 2),
		[]GroupProduct{specs[1], specs[0]})
	if err != nil || created || groupAgain.ID != group.ID || len(groupAgain.Downloads) != 2 {
		t.Fatalf("identical submission produced duplicates: %+v created=%t err=%v", groupAgain, created, err)
	}
	other, created, err := s.EnqueueDownloadGroup(ctx, "user-2", start, start.AddDate(0, 0, 2), specs)
	if err != nil || !created || other.ID == group.ID {
		t.Fatalf("different owners shared a private parent order: %+v %v", other, err)
	}
	if _, err := s.DownloadGroupOwned(ctx, group.ID, "user-2"); err == nil {
		t.Fatal("private group leaked across owners")
	}
	if _, _, err := s.EnqueueDownloadGroup(ctx, "user-1", start, start.AddDate(0, 0, 1),
		[]GroupProduct{specs[0], specs[0]}); err == nil {
		t.Fatal("duplicates accepted")
	}
	all, err := s.DownloadGroupsByOwner(ctx, "user-1", 30)
	if err != nil || len(all) != 1 {
		t.Fatalf("wrong group list: %+v %v", all, err)
	}
	if requests, err := s.DownloadRequests(ctx, 100); err != nil || len(requests) != 4 {
		t.Fatalf("atomic child count wrong: %d %v", len(requests), err)
	}
}

func TestCalendarYearBackfillOnlyUnknownAndIsIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "calendar.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	key := "modis:C1443536017-LAADS"
	if err := s.UpsertProducts(ctx, "modis", []model.Product{{ProductKey: key, SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueMissingCalendarYear(ctx, key, 2023); err != nil {
		t.Fatal(err)
	}
	var first int
	if err := s.db.QueryRow("SELECT count(*) FROM official_work WHERE product_key=?", key).Scan(&first); err != nil || first != 365 {
		t.Fatalf("missing 365-day backfill: %d %v", first, err)
	}
	if err := s.EnqueueMissingCalendarYear(ctx, key, 2023); err != nil {
		t.Fatal(err)
	}
	var again int
	if err := s.db.QueryRow("SELECT count(*) FROM official_work WHERE product_key=?", key).Scan(&again); err != nil || again != first {
		t.Fatalf("refresh requeued or duplicated pending dates: %d %v", again, err)
	}
	if _, err := s.writer.ExecContext(ctx, "DELETE FROM official_work WHERE product_key=? AND day=?", key, "2023-01-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO official_days(product_key,day,state,refreshed_at)
	 VALUES(?,?,?,?)`, key, "2023-01-01", "present", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueMissingCalendarYear(ctx, key, 2023); err != nil {
		t.Fatal(err)
	}
	var shouldNotRequeue int
	if err := s.db.QueryRow("SELECT count(*) FROM official_work WHERE product_key=? AND day=?", key, "2023-01-01").Scan(&shouldNotRequeue); err != nil || shouldNotRequeue != 0 {
		t.Fatalf("already-indexed day was requeued: %d %v", shouldNotRequeue, err)
	}
}

func TestVerifiedReuseCandidateNeedsIdentityAndUnchangedLocalMetadata(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "archives.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	path := "/archive/MYD06_L2/sample.hdf"
	if err := s.RecordVerifiedArchiveFile(ctx, path, 120, 57,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"modis", "modis:C1", "modis:G1", "asset-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO local_files(path,filename,size,mtime_ns,state,updated_at)
		VALUES(?,?,?,?,?,?)`, path, "sample.hdf", 120, 57, "ok", "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	entry, found, err := s.FindVerifiedArchiveAsset(ctx, "modis", "modis:C1", "modis:G1", "asset-1", "sample.hdf")
	if err != nil || !found || entry.Size != 120 || entry.Path != path {
		t.Fatalf("valid reuse index missed: %+v %t %v", entry, found, err)
	}
	for _, tc := range []struct{ source, collection, granule, asset, name string }{
		{"viirs", "modis:C1", "modis:G1", "asset-1", "sample.hdf"},
		{"modis", "modis:C2", "modis:G1", "asset-1", "sample.hdf"},
		{"modis", "modis:C1", "modis:G2", "asset-1", "sample.hdf"},
		{"modis", "modis:C1", "modis:G1", "asset-2", "sample.hdf"},
		{"modis", "modis:C1", "modis:G1", "asset-1", "unrelated.hdf"},
	} {
		if _, yes, err := s.FindVerifiedArchiveAsset(ctx, tc.source, tc.collection, tc.granule, tc.asset, tc.name); err != nil || yes {
			t.Fatalf("false cross-product reuse for %+v: yes=%v err=%v", tc, yes, err)
		}
	}
	if _, err := s.writer.ExecContext(ctx, "UPDATE local_files SET mtime_ns=58 WHERE path=?", path); err != nil {
		t.Fatal(err)
	}
	if _, yes, err := s.FindVerifiedArchiveAsset(ctx, "modis", "modis:C1", "modis:G1", "asset-1", "sample.hdf"); err != nil || yes {
		t.Fatalf("stale local file index was reused: %t %v", yes, err)
	}
}
