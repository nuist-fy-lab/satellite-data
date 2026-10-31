package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestDownloadTasksLegacyProjection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testDownloadTasksLegacyProjection(t, s)
}

func TestPostgresDownloadTasksLegacyProjection(t *testing.T) {
	dsn := os.Getenv("SATELLITE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("a disposable PostgreSQL instance is required")
	}
	t.Setenv("SATELLITED_POSTGRES_DSN", dsn)
	s, err := Open(filepath.Join(t.TempDir(), "unused"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testDownloadTasksLegacyProjection(t, s)
}

func testDownloadTasksLegacyProjection(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	const key = "modis:legacy-projection-test"
	if err := s.UpsertProducts(ctx, "modis", []model.Product{{ProductKey: key, SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	group, _, err := s.EnqueueDownloadGroup(ctx, "legacy-test-alice", start, start.Add(time.Hour), []GroupProduct{{Source: "modis", Provider: "modis", Collection: key}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceDownloadRequest(ctx, group.Downloads[0].ID, start.Add(time.Hour), 0, nil, true); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.EnqueueSelectedDownloadRequestOwned(ctx, "legacy-test-alice", "modis", "modis", key, start, start.Add(time.Hour), downloadfilter.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceDownloadRequest(ctx, legacy.ID, start.Add(time.Hour), 1, []DownloaderTaskRef{{ID: 987654, Key: "legacy-key", Filename: "legacy.hdf"}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueSelectedDownloadRequestOwned(ctx, "legacy-test-bob", "modis", "modis", key, start, start.Add(time.Hour), downloadfilter.Selection{}); err != nil {
		t.Fatal(err)
	}
	unowned, err := s.EnqueueDownloadRequest(ctx, "modis", "modis", key, start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var completedID int64
	for i := 0; i < 31; i++ {
		item, err := s.EnqueueSelectedDownloadRequestOwned(ctx, "legacy-test-alice", "modis", "modis", key, start.Add(time.Duration(i)*time.Hour), start.Add(time.Duration(i+1)*time.Hour), downloadfilter.Selection{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			completedID = item.ID
			if err := s.AdvanceDownloadRequest(ctx, item.ID, item.End, 0, nil, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	stamp := "2025-01-01T00:00:00Z"
	if _, err := s.writer.ExecContext(ctx, `UPDATE download_groups SET created_at=? WHERE id=?`, stamp, group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE download_requests SET requested_at=? WHERE owner_user_id=?`, stamp, "legacy-test-alice"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	count := 0
	var previous DownloadTaskListItem
	for page := 1; page <= 2; page++ {
		items, total, err := s.DownloadTasksPage(ctx, page, "legacy-test-alice", false)
		if err != nil {
			t.Fatal(err)
		}
		if total != 33 || (page == 1 && len(items) != 30) || (page == 2 && len(items) != 3) {
			t.Fatalf("page %d total=%d len=%d", page, total, len(items))
		}
		for _, item := range items {
			if count > 0 && (previous.Kind < item.Kind || (previous.Kind == item.Kind && previous.ID <= item.ID)) {
				t.Fatalf("unstable tied ordering: %s then %s", previous.Key, item.Key)
			}
			previous = item
			if seen[item.Key] {
				t.Fatalf("duplicate %s", item.Key)
			}
			seen[item.Key] = true
			if item.Kind == "legacy" && item.RequestID == completedID && item.State != "done" {
				t.Fatalf("completed legacy: %+v", item)
			}
			count++
		}
	}
	if count != 33 || !seen[fmt.Sprintf("group:%d", group.ID)] || !seen[fmt.Sprintf("legacy:%d", legacy.ID)] || !seen[fmt.Sprintf("legacy:%d", completedID)] || seen[fmt.Sprintf("legacy:%d", group.Downloads[0].ID)] {
		t.Fatalf("identities: %v", seen)
	}
	items, total, err := s.DownloadTasksPage(ctx, 1, "legacy-test-bob", false)
	if err != nil || total != 1 || len(items) != 1 || items[0].Kind != "legacy" {
		t.Fatalf("bob: %+v %d %v", items, total, err)
	}
	items, total, err = s.DownloadTasksPage(ctx, 1, "", false)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("empty owner: %+v %d %v", items, total, err)
	}
	items, total, err = s.DownloadTasksPage(ctx, 1, "", true)
	if err != nil || total < 35 || len(items) != 30 {
		t.Fatalf("admin: %d %d %v", len(items), total, err)
	}
	all := []DownloadTaskListItem{}
	for page := 1; page <= (total+29)/30; page++ {
		p, _, err := s.DownloadTasksPage(ctx, page, "", true)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, p...)
	}
	var found bool
	unownedCount := 0
	for _, item := range all {
		if item.Kind == "legacy" && item.RequestID == unowned.ID {
			unownedCount++
		}
		if item.Kind == "legacy" && item.RequestID == legacy.ID {
			found = true
			if item.State != "transferring" || item.AssetsQueued != 1 || len(item.Downloads) != 1 {
				t.Fatalf("legacy state: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("legacy transferring item absent")
	}
	if unownedCount != 1 {
		t.Fatalf("admin unowned legacy count=%d", unownedCount)
	}

	// Straddle the page boundary with variable-width RFC3339 fractional seconds.
	const timeOwner = "legacy-test-time-order"
	stamps := []string{"2025-01-01T00:00:00Z", "2025-01-01T00:00:00.05Z", "2025-01-01T00:00:00.5Z", "2025-01-01T00:00:00.005Z"}
	for i := 0; i < 32; i++ {
		item, err := s.EnqueueSelectedDownloadRequestOwned(ctx, timeOwner, "modis", "modis", key, start, start.Add(time.Hour), downloadfilter.Selection{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.writer.ExecContext(ctx, `UPDATE download_requests SET requested_at=? WHERE id=?`, stamps[i%len(stamps)], item.ID); err != nil {
			t.Fatal(err)
		}
	}
	var prior time.Time
	ordered := map[string]bool{}
	for page := 1; page <= 2; page++ {
		items, total, err := s.DownloadTasksPage(ctx, page, timeOwner, false)
		if err != nil || total != 32 || (page == 1 && len(items) != 30) || (page == 2 && len(items) != 2) {
			t.Fatalf("time page=%d count=%d total=%d err=%v", page, len(items), total, err)
		}
		for _, item := range items {
			if !prior.IsZero() && item.CreatedAt.After(prior) {
				t.Fatalf("time order: %s before %s", prior, item.CreatedAt)
			}
			if ordered[item.Key] {
				t.Fatalf("duplicate across time pages: %s", item.Key)
			}
			ordered[item.Key] = true
			prior = item.CreatedAt
		}
	}
}
