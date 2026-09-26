package state

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestBandCoverageCountsAndInvalidation(t *testing.T) {
	ctx := t.Context()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m", AreaType: "DISK", DataType: "ABI-L1b-RadF"}
	if err := store.UpsertProducts(ctx, "goes18", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2023, 6, 14, 0, 0, 0, 0, time.UTC)
	name := func(band int, suffix string) string {
		return fmt.Sprintf("OR_ABI-L1b-RadF-M6C%02d_G18_%s.nc", band, suffix)
	}
	makeGranule := func(id string, minute, second int, files ...string) model.Granule {
		assets := make([]model.Asset, 0, len(files))
		for _, file := range files {
			assets = append(assets, model.Asset{Name: file, Size: 10})
		}
		return model.Granule{ID: id, SourceID: "goes18", CollectionID: product.ProductKey,
			Start: base.Add(time.Duration(minute)*time.Minute + time.Duration(second)*time.Second), Assets: assets}
	}
	a, b, c, d := name(6, "a"), name(6, "b"), name(6, "c"), name(6, "d")
	granules := []model.Granule{
		makeGranule("first", 0, 21, name(1, "a"), a, b),
		makeGranule("duplicate", 0, 31, d),
		makeGranule("ten", 10, 21, name(1, "b"), c),
		makeGranule("hour", 60, 21, name(1, "c"), name(6, "hour")),
	}
	if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2023-06-14"}}, granules); err != nil {
		t.Fatal(err)
	}
	local := func(files ...string) []LocalFile {
		result := make([]LocalFile, 0, len(files))
		for i, file := range files {
			result = append(result, LocalFile{Path: "/archive/" + file, Filename: file, Size: 10, MtimeNS: int64(i + 1)})
		}
		return result
	}
	if _, err := store.ReplaceLocalFiles(ctx, local(a, name(6, "hour")), "partial"); err != nil {
		t.Fatal(err)
	}
	year, err := store.BandCoverageYear(ctx, product.ProductKey, 2023)
	if err != nil {
		t.Fatal(err)
	}
	if len(year.Days) != 1 || len(year.Bands) != 16 || year.Days[0][0] != 164 {
		t.Fatalf("year: %+v", year)
	}
	band := coverageBand(t, year.Days[0][12].([]BandTotals), 6)
	if band[1] != 4 || band[2] != 1 || band[3] != 2 || band[4] != 1 || band[5] != 1 || band[6] != 1 {
		t.Fatalf("partial / duplicate nominal hour: %+v", band)
	}
	day, err := store.BandCoverageDay(ctx, product.ProductKey, "2023-06-14")
	if err != nil {
		t.Fatal(err)
	}
	if day.OfficialState != 2 || day.Mode != "grid" || len(day.Times) != 4 {
		t.Fatalf("day: %+v", day)
	}
	files, err := store.BandCoverageFiles(ctx, product.ProductKey, day.Times[0][0].(string), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].State != "ok" || files[1].State != "missing" {
		t.Fatalf("files: %+v", files)
	}
	month, err := store.BandCoverageMonth(ctx, product.ProductKey, 2023, 6, 6, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(month.Days) != 30 {
		t.Fatalf("month length %d", len(month.Days))
	}
	hourly := coverageBand(t, month.Days[13][4].([]BandTotals), 6)
	if hourly[1] != 2 || hourly[2] != 1 || hourly[5] != 1 {
		t.Fatalf("hourly: %+v", hourly)
	}
	if _, err := store.ReplaceLocalFiles(ctx, local(a, b, c, d, name(6, "hour")), "complete"); err != nil {
		t.Fatal(err)
	}
	year, err = store.BandCoverageYear(ctx, product.ProductKey, 2023)
	if err != nil {
		t.Fatal(err)
	}
	band = coverageBand(t, year.Days[0][12].([]BandTotals), 6)
	if band[2] != 4 || band[4] != 2 {
		t.Fatalf("cache did not refresh: %+v", band)
	}
	if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2023-06-15"}}, nil); err != nil {
		t.Fatal(err)
	}
	missing, err := store.BandCoverageDay(ctx, product.ProductKey, "2023-06-15")
	if err != nil || missing.OfficialState != 1 || len(missing.Cells) != 0 {
		t.Fatalf("official missing: %+v %v", missing, err)
	}
	unknown, err := store.BandCoverageDay(ctx, product.ProductKey, "2023-06-16")
	if err != nil || unknown.OfficialState != 0 {
		t.Fatalf("unindexed: %+v %v", unknown, err)
	}
	if _, err := store.BandCoverageFiles(ctx, product.ProductKey, "2023-06-14T08:00:21+08:00", 6); err == nil {
		t.Fatal("non-UTC slot accepted")
	}
}

func coverageBand(t *testing.T, rows []BandTotals, band int) BandTotals {
	t.Helper()
	for _, row := range rows {
		if row[0] == band {
			return row
		}
	}
	t.Fatalf("band %d absent: %+v", band, rows)
	return BandTotals{}
}

func TestBandCoverageUnrecognizedFilenames(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}
	if err := store.UpsertProducts(t.Context(), "goes18", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOfficialBatch(t.Context(), WorkBatch{Product: product.ProductKey, Days: []string{"2023-06-14"}},
		[]model.Granule{{ID: "bad", Start: time.Date(2023, 6, 14, 0, 0, 21, 0, time.UTC), Assets: []model.Asset{{Name: "unclassified.nc"}}}}); err != nil {
		t.Fatal(err)
	}
	day, err := store.BandCoverageDay(t.Context(), product.ProductKey, "2023-06-14")
	if err != nil || day.OfficialState != 3 {
		t.Fatalf("unrecognized must not appear as missing: %+v %v", day, err)
	}
}

func TestCoverageLRUEvictsLeastRecentlyUsed(t *testing.T) {
	store := &Store{}
	for i := 0; i < coverageCacheLimit; i++ {
		key := coverageKey{product: "goes18:ABI-L1b-RadF", day: fmt.Sprintf("%04d", i)}
		store.rememberCoverage(key, coverageEntry{revision: "v1"})
	}
	first := coverageKey{product: "goes18:ABI-L1b-RadF", day: "0000"}
	second := coverageKey{product: "goes18:ABI-L1b-RadF", day: "0001"}
	if _, ok := store.cachedCoverage(first, "v1"); !ok {
		t.Fatal("initial LRU lookup failed")
	}
	store.rememberCoverage(coverageKey{product: first.product, day: "new"}, coverageEntry{revision: "v2"})
	if len(store.coverageCache) != coverageCacheLimit || store.coverageLRU.Len() != coverageCacheLimit {
		t.Fatalf("LRU exceeded bound: map=%d list=%d", len(store.coverageCache), store.coverageLRU.Len())
	}
	if _, ok := store.cachedCoverage(first, "v1"); !ok {
		t.Fatal("recently used entry evicted")
	}
	if _, ok := store.cachedCoverage(second, "v1"); ok {
		t.Fatal("oldest entry retained")
	}
	store.rememberCoverage(first, coverageEntry{revision: "v2"})
	if _, ok := store.cachedCoverage(first, "v1"); ok {
		t.Fatal("stale revision accepted")
	}
	if _, ok := store.cachedCoverage(first, "v2"); !ok {
		t.Fatal("updated revision missing")
	}
}
