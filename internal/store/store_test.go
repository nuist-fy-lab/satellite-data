package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data-downloader/internal/model"
)

func TestNewDownloaderStoreDoesNotCreateAccessTables(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "queue.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var tables int
	err = state.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('credentials','proxies','accounts')`).Scan(&tables)
	if err != nil || tables != 0 {
		t.Fatalf("downloader still initializes credential storage: tables=%d err=%v", tables, err)
	}
}

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

func TestArchiveNotificationOutboxSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, _, err := state.Enqueue(ctx, model.TaskSpec{
		Key: "earthcare-test", SourceID: "earthcare", CollectionID: "collection",
		GranuleID: "item", AssetID: "asset", Name: "file.h5",
		URL: "https://example.invalid/file.h5", RelativeDir: "PRODUCT/20260928",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Complete(ctx, task.ID, "/archive/PRODUCT/20260928/file.h5", "digest", 42); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	pending, err := state.PendingArchiveEvents(ctx, 16)
	if err != nil || len(pending) != 1 || pending[0].ID != task.ID || pending[0].ArchivePath != "/archive/PRODUCT/20260928/file.h5" {
		t.Fatalf("outbox after restart: %+v (%v)", pending, err)
	}
	if err := state.RetryArchiveEvent(ctx, task.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	pending, err = state.PendingArchiveEvents(ctx, 16)
	if err != nil || len(pending) != 0 {
		t.Fatalf("retry should defer event: %+v (%v)", pending, err)
	}
	if err := state.AcknowledgeArchiveEvent(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.Complete(ctx, task.ID, "/archive/PRODUCT/20260928/file.h5", "digest", 42); err != nil {
		t.Fatal(err)
	}
	pending, err = state.PendingArchiveEvents(ctx, 16)
	if err != nil || len(pending) != 1 {
		t.Fatalf("recompleted task should requeue event: %+v (%v)", pending, err)
	}
}
