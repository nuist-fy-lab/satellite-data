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

func TestClaimTransferPrioritizesReadyRetry(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	queued, _, err := state.Enqueue(ctx, model.TaskSpec{
		Key: "goes-queued", SourceID: "goes", CollectionID: "goes:product",
		GranuleID: "queued", AssetID: "a", Name: "queued.nc",
		URL: "https://example.invalid/queued", RelativeDir: "goes/2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, _, err := state.Enqueue(ctx, model.TaskSpec{
		Key: "goes-retry", SourceID: "goes", CollectionID: "goes:product",
		GranuleID: "retry", AssetID: "b", Name: "retry.nc",
		URL: "https://example.invalid/retry", RelativeDir: "goes/2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID >= retry.ID {
		t.Fatalf("test requires queued id < retry id: %d >= %d", queued.ID, retry.ID)
	}
	if err := state.RetryTransfer(ctx, retry.ID, errors.New("temporary timeout"), time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE tasks SET next_attempt_ns=? WHERE id=?", time.Now().Add(-time.Second).UnixNano(), retry.ID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := state.ClaimTransfer(ctx, "goes")
	if err != nil || !ok {
		t.Fatalf("claim retry ok=%v err=%v", ok, err)
	}
	if claimed.ID != retry.ID {
		t.Fatalf("claimed task=%d want ready retry=%d", claimed.ID, retry.ID)
	}
}

func TestClaimVerifyPrioritizesReadyRetry(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	verifying, _, err := state.Enqueue(ctx, model.TaskSpec{
		Key: "goes-verify", SourceID: "goes", CollectionID: "goes:product",
		GranuleID: "verify", AssetID: "a", Name: "verify.nc",
		URL: "https://example.invalid/verify", RelativeDir: "goes/2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, _, err := state.Enqueue(ctx, model.TaskSpec{
		Key: "goes-verify-retry", SourceID: "goes", CollectionID: "goes:product",
		GranuleID: "verify-retry", AssetID: "b", Name: "verify-retry.nc",
		URL: "https://example.invalid/verify-retry", RelativeDir: "goes/2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.MarkDownloaded(ctx, verifying.ID, "/tmp/verify", 1); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkDownloaded(ctx, retry.ID, "/tmp/verify-retry", 1); err != nil {
		t.Fatal(err)
	}
	if err := state.RetryVerification(ctx, retry.ID, errors.New("temporary verify error"), time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE tasks SET next_attempt_ns=? WHERE id=?", time.Now().Add(-time.Second).UnixNano(), retry.ID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := state.ClaimVerify(ctx, "goes")
	if err != nil || !ok {
		t.Fatalf("claim verify retry ok=%v err=%v", ok, err)
	}
	if claimed.ID != retry.ID {
		t.Fatalf("claimed verify task=%d want ready retry=%d", claimed.ID, retry.ID)
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
