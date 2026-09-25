package state

import (
	"context"
	"database/sql"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	_ "modernc.org/sqlite"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadClaimsAreProviderIsolated(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	start := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if _, err := store.EnqueueDownloadRequest(ctx, "goes18", "goes", "goes18:ABI-L1b-RadF", start, end); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueDownloadRequest(ctx, "modis", "modis", "modis:C123-LAADS", start, end); err != nil {
		t.Fatal(err)
	}
	modis, ok, err := store.ClaimDownloadRequest(ctx, "modis")
	if err != nil || !ok {
		t.Fatalf("claim modis ok=%v err=%v", ok, err)
	}
	if modis.ProviderID != "modis" {
		t.Fatalf("claimed provider %q", modis.ProviderID)
	}
	goes, ok, err := store.ClaimDownloadRequest(ctx, "goes")
	if err != nil || !ok {
		t.Fatalf("claim goes ok=%v err=%v", ok, err)
	}
	if goes.ProviderID != "goes" {
		t.Fatalf("claimed provider %q", goes.ProviderID)
	}
}

func TestDownloadSelectionSurvivesRestartAndMigratesOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(downloadSchema); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	item, err := store.EnqueueSelectedDownloadRequest(t.Context(), "goes18", "goes", "goes18:ABI-L1b-RadF", start, start.AddDate(0, 1, 0), downloadfilter.Selection{Band: 6, Times: []string{"01:00", "00:00"}})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item, err = store.DownloadRequest(t.Context(), item.ID)
	if err != nil || item.Selection.Band != 6 || len(item.Selection.Times) != 2 || item.Selection.Times[0] != "00:00" {
		t.Fatalf("restored=%+v err=%v", item, err)
	}
	if _, _, err := store.ClaimDownloadRequest(t.Context(), "goes"); err != nil {
		t.Fatal(err)
	}
}
