package downloadcoord

import (
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestProviderForSource(t *testing.T) {
	if got, ok := ProviderForSource("goes18"); !ok || got != "goes" {
		t.Fatalf("goes18 => %q ok=%v", got, ok)
	}
	if got, ok := ProviderForSource("viirs"); !ok || got != "viirs" {
		t.Fatalf("viirs => %q ok=%v", got, ok)
	}
	if got, ok := ProviderForSource("himawari9"); !ok || got != "himawari9" {
		t.Fatalf("himawari9 => %q ok=%v", got, ok)
	}
}

func TestArchiveRelativeDir(t *testing.T) {
	product := model.Product{
		Satellite: "GOES-18", Instrument: "ABI", Level: "L1b", DataType: "ABI-L1b-RadF",
	}
	got := archiveRelativeDir("goes18", product, time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC))
	want := "goes18/GOES-18/ABI/L1b/ABI-L1b-RadF/2026/09/24"
	if got != want {
		t.Fatalf("path=%q want=%q", got, want)
	}
}

func TestSharedArchiveRelativeDirs(t *testing.T) {
	date := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	for _, item := range []struct{ source, dataType, want string }{
		{"earthcare", "AUX_JSG_1D", "AUX_JSG_1D/20260924"},
		{"era5", "reanalysis-era5-single-levels", "CDS/reanalysis-era5-single-levels/2026/09/24"},
	} {
		if got := archiveRelativeDir(item.source, model.Product{DataType: item.dataType}, date); got != item.want {
			t.Errorf("%s: %q, want %q", item.source, got, item.want)
		}
	}
}

func TestDiscoveryRetryDelayHasJitterAndCap(t *testing.T) {
	for _, tc := range []struct {
		failure  int
		min, max time.Duration
	}{{1, 5 * time.Second, 7500 * time.Millisecond}, {2, 10 * time.Second, 15 * time.Second},
		{20, 5 * time.Minute, 5 * time.Minute}} {
		for range 16 {
			got := discoveryRetryDelay(tc.failure)
			if got < tc.min || got > tc.max {
				t.Fatalf("failure %d: delay %s outside [%s,%s]", tc.failure, got, tc.min, tc.max)
			}
		}
	}
}
