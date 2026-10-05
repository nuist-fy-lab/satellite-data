package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	_ "modernc.org/sqlite"
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

func TestDownloadRequestUpdatesRespectTimestampAndIDCursor(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	first, err := store.EnqueueDownloadRequest(ctx, "goes18", "goes", "goes18:a", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueDownloadRequest(ctx, "goes18", "goes", "goes18:b", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 5, 12, 0, 0, 123456000, time.UTC)
	if _, err := store.writer.ExecContext(ctx, "UPDATE download_requests SET updated_at=? WHERE id IN (?,?)",
		formatTime(stamp), first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	items, err := store.DownloadRequestUpdates(ctx, 10, stamp, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != second.ID {
		t.Fatalf("updates=%+v want only id=%d", items, second.ID)
	}
}

func TestDownloadRequestUpdatesReturnOldRequestAfterChange(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	old, err := store.EnqueueDownloadRequest(ctx, "goes18", "goes", "goes18:old", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cursor := time.Now().UTC()
	if _, err := store.writer.ExecContext(ctx, "UPDATE download_requests SET updated_at=? WHERE id=?",
		formatTime(cursor.Add(time.Second)), old.ID); err != nil {
		t.Fatal(err)
	}
	items, err := store.DownloadRequestUpdates(ctx, 10, cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != old.ID {
		t.Fatalf("updates=%+v want id=%d", items, old.ID)
	}
}

func TestTransferProgressDoesNotFinishDiscovery(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	start := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	request, err := store.EnqueueDownloadRequest(ctx, "goes18", "goes", "goes18:ABI-L1b-RadF", start, start.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDownloadRequest(ctx, request.ID, start.Add(time.Hour), 1, []DownloaderTaskRef{{ID: 1, Key: "sample", Filename: "sample.nc"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadTransferState(ctx, request.ID, 1, []downloader.Task{{ID: 1, State: "done"}}); err != nil {
		t.Fatal(err)
	}
	request, err = store.DownloadRequest(ctx, request.ID)
	if err != nil || request.State != "pending" || request.FilesDone != 1 || !request.CompletedAt.IsZero() {
		t.Fatalf("pending progress=%+v err=%v", request, err)
	}
	_, claimed, err := store.ClaimDownloadRequest(ctx, "goes")
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err := store.UpdateDownloadTransferState(ctx, request.ID, 1, []downloader.Task{{ID: 1, State: "done"}}); err != nil {
		t.Fatal(err)
	}
	request, err = store.DownloadRequest(ctx, request.ID)
	if err != nil || request.State != "discovering" || request.FilesDone != 1 {
		t.Fatalf("discovering progress=%+v err=%v", request, err)
	}
	if err := store.AdvanceDownloadRequest(ctx, request.ID, request.End, 0, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadTransferState(ctx, request.ID, 1, []downloader.Task{{ID: 1, State: "done"}}); err != nil {
		t.Fatal(err)
	}
	request, err = store.DownloadRequest(ctx, request.ID)
	if err != nil || request.State != "done" || request.FilesDone != 1 || request.CompletedAt.IsZero() {
		t.Fatalf("done=%+v err=%v", request, err)
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

func TestDownloadOwnershipMigrationKeepsExistingOrdersPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(downloadSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE download_requests ADD COLUMN selection_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO download_requests(source_id,provider_id,collection_id,start_ts,end_ts,cursor_ts,state,requested_at,updated_at)
	VALUES('goes18','goes','goes18:ABI-L1b-RadF','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z','2024-01-01T00:00:00Z','done','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	owned, err := store.EnqueueSelectedDownloadRequestOwned(t.Context(), "user-a", "goes18", "goes", "goes18:ABI-L1b-RadF", start, start.AddDate(0, 0, 1), downloadfilter.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DownloadRequestOwned(t.Context(), 1, "user-a"); err != sql.ErrNoRows {
		t.Fatalf("legacy order visible to user: %v", err)
	}
	if _, err := store.DownloadRequestOwned(t.Context(), owned.ID, "user-b"); err != sql.ErrNoRows {
		t.Fatalf("other user's order visible: %v", err)
	}
	items, err := store.DownloadRequestsOwned(t.Context(), 20, "user-a")
	if err != nil || len(items) != 1 || items[0].ID != owned.ID || items[0].OwnerUserID != "user-a" {
		t.Fatalf("owned order not retained: %+v err=%v", items, err)
	}
	all, err := store.DownloadRequests(t.Context(), 20)
	if err != nil || len(all) != 2 || all[1].OwnerUserID != "" {
		t.Fatalf("legacy migration changed history: %+v err=%v", all, err)
	}
}
