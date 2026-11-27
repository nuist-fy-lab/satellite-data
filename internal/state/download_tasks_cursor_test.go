package state

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestDownloadTaskCursorRoundTrip(t *testing.T) {
	encoded := EncodeDownloadTaskCursor("2026-01-02T03:04:05.123456789Z", "group", 42)
	if encoded != "2026-01-02T03:04:05|123456789|group|42" {
		t.Fatalf("unexpected encoded cursor %q", encoded)
	}
	parsed, err := ParseDownloadTaskCursor(encoded)
	if err != nil {
		t.Fatalf("ParseDownloadTaskCursor failed: %v", err)
	}
	if parsed.StampSec != "2026-01-02T03:04:05" || parsed.StampFrac != "123456789" || parsed.Kind != "group" || parsed.ID != 42 {
		t.Fatalf("unexpected parsed cursor: %+v", parsed)
	}

	wholeSec := EncodeDownloadTaskCursor("2026-01-02T03:04:05Z", "legacy", 7)
	if wholeSec != "2026-01-02T03:04:05|000000000|legacy|7" {
		t.Fatalf("unexpected whole-second cursor %q", wholeSec)
	}
	if _, err := ParseDownloadTaskCursor(wholeSec); err != nil {
		t.Fatalf("ParseDownloadTaskCursor whole-second failed: %v", err)
	}

	for _, bad := range []string{
		"",
		"not-a-cursor",
		"2026-01-02|000000000|group|1",
		"2026-01-02T03:04:05|12345|group|1",
		"2026-01-02T03:04:05|12345678a|group|1",
		"2026-01-02T03:04:05|000000000|other|1",
		"2026-01-02T03:04:05|000000000|group|0",
		"2026-01-02T03:04:05|000000000|group|-3",
	} {
		if _, err := ParseDownloadTaskCursor(bad); err == nil {
			t.Fatalf("expected invalid cursor %q to fail", bad)
		}
	}
}

func TestDownloadTasksCursorPageParityAndWholeSecondTieBreak(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	const key = "modis:cursor-test"
	if err := s.UpsertProducts(ctx, "modis", []model.Product{{ProductKey: key, SourceID: "modis"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	g1, _, err := s.EnqueueDownloadGroup(ctx, "cursor-alice", start, start.Add(time.Hour), []GroupProduct{{Source: "modis", Provider: "modis", Collection: key}})
	if err != nil {
		t.Fatal(err)
	}
	g2, _, err := s.EnqueueDownloadGroup(ctx, "cursor-alice", start.Add(2*time.Hour), start.Add(3*time.Hour), []GroupProduct{{Source: "modis", Provider: "modis", Collection: key}})
	if err != nil {
		t.Fatal(err)
	}
	l1, err := s.EnqueueSelectedDownloadRequestOwned(ctx, "cursor-alice", "modis", "modis", key, start.Add(4*time.Hour), start.Add(5*time.Hour), downloadfilter.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	l2, err := s.EnqueueSelectedDownloadRequestOwned(ctx, "cursor-alice", "modis", "modis", key, start.Add(6*time.Hour), start.Add(7*time.Hour), downloadfilter.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnqueueDownloadGroup(ctx, "cursor-bob", start.Add(8*time.Hour), start.Add(9*time.Hour), []GroupProduct{{Source: "modis", Provider: "modis", Collection: key}}); err != nil {
		t.Fatal(err)
	}

	// Tie groups and ungrouped requests onto the exact same whole-second timestamp
	// to verify SQL padding and cursor tie-breaking ('group' vs 'legacy', id DESC).
	const sameSecond = "2026-02-02T12:00:00Z"
	if _, err := s.writer.ExecContext(ctx, `UPDATE download_groups SET created_at=? WHERE id IN (?,?)`, sameSecond, g1.ID, g2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE download_requests SET requested_at=? WHERE id IN (?,?)`, sameSecond, l1.ID, l2.ID); err != nil {
		t.Fatal(err)
	}

	pageItems, pageTotal, err := s.DownloadTasksPage(ctx, 1, "cursor-alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if pageTotal != 4 || len(pageItems) != 4 {
		t.Fatalf("unexpected page counts total=%d len=%d", pageTotal, len(pageItems))
	}

	var walkedKeys []string
	var cursor *DownloadTaskCursor
	for steps := 0; steps < 10; steps++ {
		items, total, hasMore, nextCursor, err := s.DownloadTasksCursorPage(ctx, cursor, 1, "cursor-alice", false)
		if err != nil {
			t.Fatalf("cursor step %d failed: %v", steps, err)
		}
		if total != 4 || len(items) != 1 {
			t.Fatalf("step %d unexpected total=%d len=%d", steps, total, len(items))
		}
		walkedKeys = append(walkedKeys, items[0].Key)
		if !hasMore {
			if nextCursor != "" {
				t.Fatalf("expected empty nextCursor at end, got %q", nextCursor)
			}
			break
		}
		parsed, err := ParseDownloadTaskCursor(nextCursor)
		if err != nil {
			t.Fatalf("parse nextCursor %q: %v", nextCursor, err)
		}
		cursor = &parsed
	}

	var expectedKeys []string
	for _, item := range pageItems {
		expectedKeys = append(expectedKeys, item.Key)
	}
	if !reflect.DeepEqual(walkedKeys, expectedKeys) {
		t.Fatalf("cursor order %v != page order %v", walkedKeys, expectedKeys)
	}

	adminItems, adminTotal, hasMore, _, err := s.DownloadTasksCursorPage(ctx, nil, 10, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if adminTotal != 5 || len(adminItems) != 5 || hasMore {
		t.Fatalf("unexpected admin cursor result total=%d len=%d hasMore=%v", adminTotal, len(adminItems), hasMore)
	}
}
