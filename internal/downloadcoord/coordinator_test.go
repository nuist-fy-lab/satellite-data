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
