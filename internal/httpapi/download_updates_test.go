package httpapi

import (
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestDownloadCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 34, 56, 123456789, time.UTC)
	raw := downloadCursor(at, 42)
	gotAt, gotID, err := parseDownloadCursor(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !gotAt.Equal(at) || gotID != 42 {
		t.Fatalf("cursor round trip at=%v id=%d", gotAt, gotID)
	}
}

func TestSummarizeDownloadsUsesDisplayFieldsOnly(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	updated := start.Add(time.Hour)
	items := []state.DownloadRequest{{
		ID: 22, CollectionID: "goes18:ABI-L1b-RadC", State: "transferring",
		FilesDone: 710, AssetsQueued: 8637, Start: start, End: start.AddDate(0, 1, 0),
		Selection: downloadfilter.Selection{Band: 2, Times: []string{"00:00", "00:10"}},
		LastError: "temporary", UpdatedAt: updated,
	}}
	summary, cursorAt, cursorID := summarizeDownloads(items)
	if len(summary) != 1 {
		t.Fatalf("summary len=%d", len(summary))
	}
	if summary[0].Band != 2 || summary[0].TimesCount != 2 || summary[0].FilesDone != 710 || summary[0].AssetsQueued != 8637 {
		t.Fatalf("summary=%+v", summary[0])
	}
	if !cursorAt.Equal(updated) || cursorID != 22 {
		t.Fatalf("cursor at=%v id=%d", cursorAt, cursorID)
	}
}
