package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestAutonomousIndexAndRecountWithoutCalendarVisit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "calendar.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := "modis:C1443536017-LAADS"
	if err := s.UpsertProducts(ctx, "modis", []model.Product{{ProductKey: key, SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProducts(ctx, "earthcare", []model.Product{{ProductKey: "earthcare:sparse", SourceID: "earthcare"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.EnqueueDownloadRequest(ctx, "modis", "modis", key, start, start.AddDate(0, 0, 4)); err != nil {
		t.Fatal(err)
	}
	// One fully indexed day but no derived summary simulates an interrupted
	// index migration. The auto-maintainer must recount it without HTTP GET.
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO official_days(product_key,day,state,refreshed_at)
	VALUES(?,?,?,?)`, key, "2023-01-01", "official_missing", "2023-01-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// A failed remote lookup is already scheduled with exponential backoff.
	// It must never be reset or duplicated by the periodic sweeper.
	const postponed int64 = 1893456000000000000
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO official_work(
	product_key,day,state,priority,failure_count,next_attempt_ns,requested_at,updated_at)
	VALUES(?,?,'pending',7,4,?,'2023-01-01','2023-01-01')`, key, "2023-01-02", postponed); err != nil {
		t.Fatal(err)
	}
	// Sparse coverage elsewhere does not imply a full year's worth of
	// provider requests for all 100+ EarthCARE collections.
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO official_days(product_key,day,state,refreshed_at)
	VALUES(?,?,?,?)`, "earthcare:sparse", "2023-01-01", "present", "2023-01-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2023, 5, 3, 14, 0, 0, 0, time.UTC)
	got, err := s.AutoMaintainCalendar(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackedYears != 1 || got.IndexedDaysQueued != 121 || got.CoverageDaysRecounted != 2 {
		t.Fatalf("no autonomous indexing/recount, or unbounded sparse product scan: %+v", got)
	}
	var work int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM official_work WHERE product_key=?", key).Scan(&work); err != nil || work != 122 {
		t.Fatalf("unexpected queued days: %d %v", work, err)
	}
	var failures, priority int
	var wait int64
	if err := s.db.QueryRowContext(ctx, `SELECT failure_count,priority,next_attempt_ns FROM official_work
	 WHERE product_key=? AND day='2023-01-02'`, key).Scan(&failures, &priority, &wait); err != nil ||
		failures != 4 || priority != 7 || wait != postponed {
		t.Fatalf("retry backoff lost: failures=%d priority=%d wait=%d error=%v", failures, priority, wait, err)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM day_summary WHERE product_key=?", key).Scan(&total); err != nil || total != 1 {
		t.Fatalf("missing daily coverage recount: %d %v", total, err)
	}
	// Idempotency: polling the same period or restarting the maintainer
	// never refreshes already indexed days or resets retries.
	again, err := s.AutoMaintainCalendar(ctx, now)
	if err != nil || again.IndexedDaysQueued != 0 || again.CoverageDaysRecounted != 0 {
		t.Fatalf("duplicate background work: %+v error=%v", again, err)
	}
	// The UTC day rolling over autonomously indexes the new day.
	tomorrow, err := s.AutoMaintainCalendar(ctx, now.Add(24*time.Hour))
	if err != nil || tomorrow.IndexedDaysQueued != 1 {
		t.Fatalf("next calendar day not automatically discovered: %+v %v", tomorrow, err)
	}
	var sparse int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM official_work WHERE product_key='earthcare:sparse'").Scan(&sparse); err != nil || sparse != 0 {
		t.Fatalf("sparse EarthCARE source flooded with requests: %d %v", sparse, err)
	}
}

func TestAutoRecountRepairsStaleDerivedSummaryWithoutRemoteLookup(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "recount.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := "modis:C1"
	if err := s.UpsertProducts(ctx, "modis", []model.Product{{ProductKey: product, SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO official_days(product_key,day,state,refreshed_at)
	 VALUES(?,?,?,?)`, product, "2023-01-01", "official_missing", "2026-10-08T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO day_summary(product_key,day,status,updated_at)
	 VALUES(?,?,?,?)`, product, "2023-01-01", "unknown", "2023-01-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	count, err := s.AutoRecountStaleCalendar(ctx, 24)
	if err != nil || count != 1 {
		t.Fatalf("stale daily summary not rebuilt: %d %v", count, err)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM day_summary WHERE product_key=? AND day=?`, product, "2023-01-01").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "unknown" {
		t.Fatalf("summary still stale: %s", status)
	}
	count, err = s.AutoRecountStaleCalendar(ctx, 24)
	if err != nil || count != 0 {
		t.Fatalf("unnecessary repeated recount: %d %v", count, err)
	}
}

func TestTrackedCalendarContinuesIntoNewUTCYear(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "rollover.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := "modis:active"
	if err := s.UpsertProducts(ctx, "modis", []model.Product{{ProductKey: product, SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2023, 9, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 90; index++ {
		day := start.AddDate(0, 0, index).Format(time.DateOnly)
		if _, err := s.writer.ExecContext(ctx, `INSERT INTO official_days(product_key,day,state,refreshed_at)
		VALUES(?,?,?,?)`, product, day, "present", "2023-12-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	years, err := s.autoCalendarYears(ctx, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	found2023, found2024 := false, false
	for _, item := range years {
		if item.product == product && item.year == 2023 {
			found2023 = true
		}
		if item.product == product && item.year == 2024 {
			found2024 = true
		}
	}
	if !found2023 || !found2024 {
		t.Fatalf("UTC new year lost autonomous indexing: %+v", years)
	}
}
