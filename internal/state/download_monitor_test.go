package state

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
)

func TestDownloadMonitorSamplesIsolateAggregateAndPrune(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	start := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for index, providers := range [][]downloader.ProviderStatus{
		{{SourceID: "goes", Enabled: true, BytesPerSecond: 1024, ActiveTransfers: 2, AdaptiveConcurrency: 4, States: map[string]int{"queued": 5, "retry_wait": 1, "verifying": 3, "verify_wait": 1, "promoting": 2}}, {SourceID: "viirs", Enabled: true, BytesPerSecond: 512, ActiveTransfers: 1, AdaptiveConcurrency: 2}},
		{{SourceID: "goes", Enabled: true, BytesPerSecond: 3072, ActiveTransfers: 3, AdaptiveConcurrency: 4, States: map[string]int{"queued": 2}}},
		{{SourceID: "goes", Enabled: false, BytesPerSecond: math.NaN(), ActiveTransfers: -1, AdaptiveConcurrency: 1}},
	} {
		if err := store.RecordDownloadSamples(ctx, start.Add(time.Duration(index*5)*time.Second), providers); err != nil {
			t.Fatal(err)
		}
	}
	series, err := store.DownloadSeries(ctx, start, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(series["goes"]) != 2 || series["goes"][0].BytesPerSecond != 2048 || series["goes"][0].ActiveTransfers != 3 || series["goes"][0].Queued != 6 ||
		series["goes"][0].Verifying != 3 || series["goes"][0].VerifyWait != 1 || series["goes"][0].Promoting != 2 || len(series["viirs"]) != 1 {
		t.Fatalf("series=%+v", series)
	}
	latest, err := store.LatestDownloadSamples(ctx)
	if err != nil || len(latest) != 2 || latest[0].SourceID != "goes" || latest[0].BytesPerSecond != 0 || latest[0].ActiveTransfers != 0 || latest[0].AdaptiveConcurrency != 0 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if err := store.DeleteDownloadSamplesBefore(ctx, start.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	series, err = store.DownloadSeries(ctx, start, 10*time.Second)
	if err != nil || len(series["goes"]) != 1 || len(series["viirs"]) != 0 {
		t.Fatalf("pruned series=%+v err=%v", series, err)
	}
	if _, err := store.DownloadSeries(ctx, start, 0); err == nil {
		t.Fatal("accepted invalid bucket")
	}
}

func TestDownloadMonitorSchemaMigratesLegacyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE download_monitor_samples (
		  provider_id TEXT NOT NULL,
		  sampled_at_ns INTEGER NOT NULL,
		  bytes_per_second REAL NOT NULL,
		  active_transfers INTEGER NOT NULL,
		  adaptive_concurrency INTEGER NOT NULL,
		  queued INTEGER NOT NULL,
		  verifying INTEGER NOT NULL,
		  PRIMARY KEY(provider_id,sampled_at_ns)
		)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	rows, err := store.db.Query(`PRAGMA table_info(download_monitor_samples)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	if !found["verify_wait"] || !found["promoting"] {
		t.Fatalf("legacy monitor schema was not migrated: %+v", found)
	}
}
