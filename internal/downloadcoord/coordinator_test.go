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
	if got, ok := ProviderForSource("iasi"); !ok || got != "iasi" {
		t.Fatalf("iasi => %q ok=%v", got, ok)
	}
	if got, ok := ProviderForSource("iasi-ng"); !ok || got != "iasi-ng" {
		t.Fatalf("iasi-ng => %q ok=%v", got, ok)
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
	want := "goes18/GOES-18/ABI/L1B/ABI-L1b-RadF/2026/09/24"
	if got != want {
		t.Fatalf("path=%q want=%q", got, want)
	}
}

func TestArchiveRelativeDirKeepsAnExplicitLevelSlotWhenUnspecified(t *testing.T) {
	product := model.Product{
		Satellite: "MTG", Instrument: "FCI", DataType: "EO:EUM:DAT:1089",
	}
	got := archiveRelativeDir("mtg", product, time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC))
	want := "mtg/MTG/FCI/Unspecified/EO_EUM_DAT_1089/2026/09/24"
	if got != want {
		t.Fatalf("path=%q want=%q", got, want)
	}
}

func TestIASIArchiveUsesGranulePlatformInsteadOfCollectionPlatformGroup(t *testing.T) {
	product := model.Product{
		Satellite: "Metop", Instrument: "IASI", Level: "L1c", DataType: "EO:EUM:DAT:METOP:IASIL1C-ALL",
	}
	granule := model.Granule{
		Start:    time.Date(2025, 6, 7, 0, 0, 0, 0, time.UTC),
		Metadata: map[string]string{"satellite": "Metop-B"},
	}
	got := archiveRelativeDirForGranule("iasi", product, granule)
	want := "iasi/Metop-B/IASI/L1C/EO_EUM_DAT_METOP_IASIL1C-ALL/2025/06/07"
	if got != want {
		t.Fatalf("path=%q want=%q", got, want)
	}
}

func TestIASINGArchiveUsesGranulePlatform(t *testing.T) {
	product := model.Product{
		Satellite: "Metop-SG-A1", Instrument: "IASI-NG", Level: "L1c", DataType: "EO:EUM:DAT:0442",
	}
	granule := model.Granule{
		Start:    time.Date(2026, 10, 7, 6, 42, 37, 0, time.UTC),
		Metadata: map[string]string{"satellite": "Metop-SG-A1"},
	}
	got := archiveRelativeDirForGranule("iasi-ng", product, granule)
	want := "iasi-ng/Metop-SG-A1/IASI-NG/L1C/EO_EUM_DAT_0442/2026/10/07"
	if got != want {
		t.Fatalf("path=%q want=%q", got, want)
	}
}

func TestArchiveRelativeDirNormalizesNumericProcessingLevels(t *testing.T) {
	product := model.Product{
		Satellite: "CALIPSO", Instrument: "CALIOP", Level: "2", DataType: "CAL_LID_L2_01kmCLay-Standard-V5-00",
	}
	got := archiveRelativeDir("calipso", product, time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC))
	want := "calipso/CALIPSO/CALIOP/L2/CAL_LID_L2_01kmCLay-Standard-V5-00/2026/09/24"
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
