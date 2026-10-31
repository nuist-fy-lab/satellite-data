package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestCalendarSparseDownloadSelections(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 5, 0, 0, time.UTC)
	end := time.Date(2024, 1, 3, 0, 5, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		selection downloadfilter.Selection
		want      [4]bool
	}{
		{"granule-id", downloadfilter.Selection{GranuleIDs: []string{"cloudsat:actual-scene"}}, [4]bool{}},
		{"range", downloadfilter.Selection{}, [4]bool{true, true, true, true}},
		{"scenes", downloadfilter.Selection{Scenes: map[string][]string{"2024-01-01": {"00:00", "00:05"}, "2024-01-02": {"00:10"}, "2024-01-03": {"00:05"}}}, [4]bool{true, false, true, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			const product = "cloudsat:test"
			if err := s.UpsertProducts(ctx, "cloudsat", []model.Product{{ProductKey: product, SourceID: "cloudsat"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnqueueSelectedDownloadRequest(ctx, "cloudsat", "cloudsat", product, start, end, tc.selection); err != nil {
				t.Fatal(err)
			}
			items, err := s.activeCalendarDownloads(ctx, product, start, end)
			if err != nil {
				t.Fatal(err)
			}
			checks := []time.Time{start, start.Add(5 * time.Minute), time.Date(2024, 1, 2, 0, 10, 0, 0, time.UTC), end}
			for i, at := range checks {
				got := items[at.Format(time.DateOnly)].matches(at, 0)
				if got != tc.want[i] {
					t.Errorf("%s: got %v want %v", at, got, tc.want[i])
				}
			}
		})
	}
}

func TestCalendarGranuleIDPreservesActualProcessingFile(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "cloudsat:test", SourceID: "cloudsat", Cadence: "5m"}
	if err := s.UpsertProducts(ctx, "cloudsat", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	granules := []model.Granule{
		{ID: "selected", Start: at, Assets: []model.Asset{{Name: "selected.hdf", Size: 100}}},
		{ID: "other", Start: at.Add(5 * time.Minute), Assets: []model.Asset{{Name: "other.hdf", Size: 100}}},
	}
	if err := s.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2024-01-01"}}, granules); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueSelectedDownloadRequest(ctx, "cloudsat", "cloudsat", product.ProductKey, at, at.AddDate(0, 0, 1), downloadfilter.Selection{GranuleIDs: []string{"selected"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceLocalFiles(ctx, []LocalFile{{Path: "/archive/selected.hdf", Filename: "selected.hdf", Size: 10, MtimeNS: 1, State: "processing"}}, "processing"); err != nil {
		t.Fatal(err)
	}
	day, err := s.Day(ctx, product.ProductKey, "2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Slots) != 2 || day.Slots[0].Status != "processing" || day.Slots[1].Status != "missing" {
		t.Fatalf("unexpected slots: %+v", day.Slots)
	}
}

func TestCalendarSceneSecondsAndBoundaries(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "cloudsat:seconds", SourceID: "cloudsat"}
	if err := s.UpsertProducts(ctx, "cloudsat", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var granules []model.Granule
	for _, tc := range []struct {
		id     string
		offset time.Duration
	}{{"minute", 2 * time.Minute}, {"seconds", 2*time.Minute + 30*time.Second}, {"next", 3 * time.Minute}} {
		granules = append(granules, model.Granule{ID: tc.id, Start: day.Add(tc.offset), Assets: []model.Asset{{Name: tc.id + ".hdf", Size: 100}}})
	}
	if err := s.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2024-01-01"}}, granules); err != nil {
		t.Fatal(err)
	}
	start, end := day.Add(2*time.Minute+30*time.Second), day.Add(4*time.Minute+30*time.Second)
	selection := downloadfilter.Selection{Scenes: map[string][]string{"2024-01-01": {"00:02:29", "00:02:30", "00:04:30", "00:05:00"}}}
	if _, err := s.EnqueueSelectedDownloadRequest(ctx, "cloudsat", "cloudsat", product.ProductKey, start, end, selection); err != nil {
		t.Fatal(err)
	}
	intent, err := s.calendarIntentForDay(ctx, product.ProductKey, "2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset time.Duration
		want   bool
	}{
		{2 * time.Minute, false}, {2*time.Minute + 29*time.Second, false}, {2*time.Minute + 30*time.Second, true},
		{2*time.Minute + 30*time.Second + time.Nanosecond, false}, {3 * time.Minute, false}, {4*time.Minute + 30*time.Second, false},
	} {
		at := day.Add(tc.offset)
		if got := intent.matches(at, 0); got != tc.want || intent.matchesAny(at) != tc.want {
			t.Errorf("%s: matches=%v matchesAny=%v want %v", at, got, intent.matchesAny(at), tc.want)
		}
	}
	detail, err := s.Day(ctx, product.ProductKey, "2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Slots) != 3 || detail.Slots[0].Status != "missing" || detail.Slots[1].Status != "processing" || detail.Slots[2].Status != "missing" {
		t.Fatalf("unexpected slots: %+v", detail.Slots)
	}
}

func TestCalendarSceneZeroSecondsCanonicalMinute(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "cloudsat:zero", SourceID: "cloudsat"}
	if err := s.UpsertProducts(ctx, "cloudsat", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.EnqueueSelectedDownloadRequest(ctx, "cloudsat", "cloudsat", product.ProductKey, day, day.AddDate(0, 0, 1), downloadfilter.Selection{Scenes: map[string][]string{"2024-01-01": {"00:02:00", "00:02"}}}); err != nil {
		t.Fatal(err)
	}
	intent, err := s.calendarIntentForDay(ctx, product.ProductKey, "2024-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if intent.count() != 1 || !intent.matches(day.Add(2*time.Minute+30*time.Second), 0) || intent.matches(day.Add(3*time.Minute), 0) {
		t.Fatalf("zero-second minute semantics: %+v", intent)
	}
}
