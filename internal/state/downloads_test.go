package state

import (
	"context"
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
