package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
)

func TestProviderClaimsAreIsolated(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	for _, source := range []string{"goes", "modis"} {
		_, _, err := state.Enqueue(ctx, model.TaskSpec{
			Key: source + "-1", SourceID: source, CollectionID: source + ":product",
			GranuleID: "g", AssetID: "a", Name: source + ".dat",
			URL: "https://example.invalid/" + source, RelativeDir: source + "/2026",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	task, ok, err := state.ClaimTransfer(ctx, "modis")
	if err != nil || !ok {
		t.Fatalf("claim modis ok=%v err=%v", ok, err)
	}
	if task.SourceID != "modis" {
		t.Fatalf("claimed %q from modis queue", task.SourceID)
	}
	goes, ok, err := state.ClaimTransfer(ctx, "goes")
	if err != nil || !ok {
		t.Fatalf("claim goes ok=%v err=%v", ok, err)
	}
	if goes.SourceID != "goes" {
		t.Fatalf("claimed %q from goes queue", goes.SourceID)
	}
}

func TestEnqueueRefreshesStaleAssetURL(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	original := model.TaskSpec{Key: "same", SourceID: "modis", CollectionID: "c", GranuleID: "g", AssetID: "a", Name: "x.nc", URL: "https://old.invalid/x.nc", AuthRef: "laads", RelativeDir: "modis/x", Size: 1}
	task, created, err := state.Enqueue(ctx, original)
	if err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	if err := state.WaitForAuth(ctx, task.ID, errors.New("old auth failure")); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.URL = "https://ladsweb.modaps.eosdis.nasa.gov/archive/allData/x.nc"
	updated.Size = 2
	refreshed, created, err := state.Enqueue(ctx, updated)
	if err != nil || created {
		t.Fatalf("refresh created=%v err=%v", created, err)
	}
	if refreshed.URL != updated.URL || refreshed.Size != 2 || refreshed.State != "queued" || refreshed.LastError != "" {
		t.Fatalf("unexpected refreshed task: %#v", refreshed)
	}
}
