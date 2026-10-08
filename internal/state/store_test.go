package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestPersistentCatalogAndHourlyCompleteness(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	product := model.Product{
		ProductKey: "goes18:ABI-L2-CMIPF", SourceID: "goes18", OfficialID: "ABI-L2-CMIPF",
		Satellite: "GOES-18", Instrument: "ABI", Level: "L2", DataType: "ABI-L2-CMIPF",
		AreaType: "DISK", DisplayName: "Cloud and Moisture Imagery Full Disk", Cadence: "10m",
		AccessTier: model.TierAnonymous,
	}
	if err := store.UpsertProducts(ctx, "goes18", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := "2026-09-01"
	start, _ := time.Parse(time.DateOnly, day)
	granules := make([]model.Granule, 0, 144)
	for slot := 0; slot < 144; slot++ {
		at := start.Add(time.Duration(slot*10) * time.Minute)
		name := fmt.Sprintf("slot-%03d.nc", slot)
		granules = append(granules, model.Granule{
			ID: fmt.Sprintf("g-%03d", slot), SourceID: "goes18", CollectionID: product.ProductKey, Start: at,
			Assets: []model.Asset{{ID: name, Name: name, Size: 1}},
		})
	}
	batch := WorkBatch{Product: product.ProductKey, Days: []string{day}}
	if err := store.SaveOfficialBatch(ctx, batch, granules); err != nil {
		t.Fatal(err)
	}

	hourly := make([]LocalFile, 0, 24)
	for slot := 0; slot < 144; slot += 6 {
		name := fmt.Sprintf("slot-%03d.nc", slot)
		hourly = append(hourly, LocalFile{Path: "/archive/" + name, Filename: name, Size: 1, MtimeNS: int64(slot + 1)})
	}
	if _, err := store.ReplaceLocalFiles(ctx, hourly, "hourly"); err != nil {
		t.Fatal(err)
	}
	year, err := store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	got := findYearDay(t, year, day)
	if got.Status != "hourly_complete" || got.HourlyLocalOKCount != 24 || got.LocalOKCount != 24 {
		t.Fatalf("hourly summary=%+v", got)
	}

	all := make([]LocalFile, 0, 144)
	for slot := 0; slot < 144; slot++ {
		name := fmt.Sprintf("slot-%03d.nc", slot)
		all = append(all, LocalFile{Path: "/archive/" + name, Filename: name, Size: 1, MtimeNS: int64(slot + 1)})
	}
	if _, err := store.ReplaceLocalFiles(ctx, all, "all"); err != nil {
		t.Fatal(err)
	}
	year, err = store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	got = findYearDay(t, year, day)
	if got.Status != "complete" || got.LocalOKCount != 144 {
		t.Fatalf("complete summary=%+v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	products, err := reopened.Products(ctx, "goes18")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].ProductKey != product.ProductKey || !products[0].LocalCovered {
		t.Fatalf("persisted products=%+v", products)
	}
}

func TestCompleteWithOfficialGap(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{
		ProductKey: "himawari9:AHI-L1b-FLDK", SourceID: "himawari9", OfficialID: "AHI-L1b-FLDK",
		Satellite: "Himawari-9", Instrument: "AHI", Level: "L1b", DataType: "AHI-L1b-FLDK",
		AreaType: "DISK", DisplayName: "AHI Full Disk", Cadence: "10m", AccessTier: model.TierAnonymous,
	}
	if err := store.UpsertProducts(ctx, "himawari9", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := "2026-09-02"
	start, _ := time.Parse(time.DateOnly, day)
	var granules []model.Granule
	var files []LocalFile
	for slot := 0; slot < 144; slot++ {
		if slot == 7 {
			continue
		}
		at := start.Add(time.Duration(slot*10) * time.Minute)
		name := fmt.Sprintf("h-%03d.dat", slot)
		granules = append(granules, model.Granule{
			ID: fmt.Sprintf("h-%03d", slot), SourceID: "himawari9", CollectionID: product.ProductKey, Start: at,
			Assets: []model.Asset{{ID: name, Name: name, Size: 1}},
		})
		files = append(files, LocalFile{Path: "/archive/" + name, Filename: name, Size: 1, MtimeNS: int64(slot + 1)})
	}
	if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{day}}, granules); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceLocalFiles(ctx, files, "gap"); err != nil {
		t.Fatal(err)
	}
	year, err := store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	got := findYearDay(t, year, day)
	if got.Status != "complete_official_gap" || got.OfficialGapCount != 1 {
		t.Fatalf("gap summary=%+v", got)
	}
}

func TestRecoverOfficialWork(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{
		ProductKey: "goes18:ABI-L2-CMIPF", SourceID: "goes18", OfficialID: "ABI-L2-CMIPF",
		Satellite: "GOES-18", Instrument: "ABI", Level: "L2", DataType: "ABI-L2-CMIPF",
		AreaType: "DISK", DisplayName: "Cloud and Moisture Imagery Full Disk", Cadence: "10m",
		AccessTier: model.TierAnonymous,
	}
	if err := store.UpsertProducts(ctx, "goes18", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if err := store.EnqueueRange(ctx, product.ProductKey, start, start.AddDate(0, 0, 1), 10); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimBatch(ctx, 7)
	if err != nil || len(first.Days) != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if err := store.RecoverOfficialWork(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimBatch(ctx, 7)
	if err != nil || len(second.Days) != 1 || second.Days[0] != "2026-09-20" {
		t.Fatalf("recovered claim=%+v err=%v", second, err)
	}
}

func TestLocalScanUpdatesOnlyAffectedDaysWithoutSizeOnlyCorruption(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "goes19:test", SourceID: "goes19", Cadence: "24h"}
	if err := store.UpsertProducts(ctx, "goes19", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	first := "2026-09-23"
	second := "2026-09-24"
	for _, day := range []string{first, second} {
		at, _ := time.Parse(time.DateOnly, day)
		name := day + ".nc"
		if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{day}}, []model.Granule{{
			ID: name, Start: at, Assets: []model.Asset{{Name: name, Size: 10}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	files := []LocalFile{
		{Path: "/archive/first.nc", Filename: first + ".nc", Size: 10, MtimeNS: 1},
		{Path: "/archive/second.nc", Filename: second + ".nc", Size: 10, MtimeNS: 1},
	}
	if _, err := store.ReplaceLocalFiles(ctx, files, "initial"); err != nil {
		t.Fatal(err)
	}
	before, err := store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	secondUpdatedAt := *findYearDay(t, before, second).UpdatedAt
	files[0].Size = 5
	if _, err := store.ReplaceLocalFiles(ctx, files, "size-changed"); err != nil {
		t.Fatal(err)
	}
	after, err := store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	changed := findYearDay(t, after, first)
	if changed.Status != "complete" || changed.ErrorCount != 0 || changed.LocalOKCount != 1 {
		t.Fatalf("size-only change falsely marked corruption=%+v", changed)
	}
	if got := findYearDay(t, after, second); got.Status != "complete" || *got.UpdatedAt != secondUpdatedAt {
		t.Fatalf("unaffected day changed=%+v", got)
	}
	files[0].State = "error"
	files[0].Detail = "invalid file header"
	if _, err := store.ReplaceLocalFiles(ctx, files, "invalid-header"); err != nil {
		t.Fatal(err)
	}
	broken, err := store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := findYearDay(t, broken, first); got.Status != "error" || got.ErrorCount != 1 {
		t.Fatalf("real format error was hidden: %+v", got)
	}
	if _, err := store.ReplaceLocalFiles(ctx, files[1:], "removed-first"); err != nil {
		t.Fatal(err)
	}
	after, err = store.Year(ctx, product.ProductKey, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := findYearDay(t, after, first); got.Status != "missing" || got.MissingCount != 1 {
		t.Fatalf("removed file summary=%+v", got)
	}
}

func TestCatalogSizeDoesNotAssertIntegrityForAnyProduct(t *testing.T) {
	for _, source := range []string{"modis", "viirs", "goes18", "earthcare", "era5", "himawari9"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			product := model.Product{ProductKey: source + ":test", SourceID: source, Cadence: "event"}
			if err := store.UpsertProducts(ctx, source, []model.Product{product}); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2023, 1, 1, 5, 30, 0, 0, time.UTC)
			filename := "cloud-granule.hdf"
			if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2023-01-01"}}, []model.Granule{{
				ID: "granule", Start: at, Assets: []model.Asset{{Name: filename, Size: 112559492}},
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReplaceLocalFiles(ctx, []LocalFile{{
				Path: "/archive/" + filename, Filename: filename, Size: 112559493, MtimeNS: 1, State: "ok",
			}}, "rounded-size"); err != nil {
				t.Fatal(err)
			}
			result, err := store.Year(ctx, product.ProductKey, 2023)
			if err != nil {
				t.Fatal(err)
			}
			day := findYearDay(t, result, "2023-01-01")
			if day.ErrorCount != 0 || day.LocalOKCount != 1 || day.Status != "complete" {
				t.Fatalf("catalogue byte count falsely marked file corrupt: %+v", day)
			}
		})
	}
}

func TestUnchangedBackgroundScanHealsOldSizeOnlyErrors(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "earthcare:history", SourceID: "earthcare", Cadence: "event"}
	if err := store.UpsertProducts(ctx, "earthcare", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := "2025-06-23"
	at, _ := time.Parse(time.DateOnly, day)
	if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{day}}, []model.Granule{{
		ID: "historical", Start: at, Assets: []model.Asset{{Name: "cloud.h5", Size: 99}},
	}}); err != nil {
		t.Fatal(err)
	}
	files := []LocalFile{{Path: "/archive/cloud.h5", Filename: "cloud.h5", Size: 123, MtimeNS: 7, State: "ok"}}
	if _, err := store.ReplaceLocalFiles(ctx, files, "unchanged"); err != nil {
		t.Fatal(err)
	}
	_, err = store.writer.ExecContext(ctx, `UPDATE local_slots SET state='error',detail='local size differs from official asset' WHERE product_key=?`, product.ProductKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.writer.ExecContext(ctx, `UPDATE day_summary SET status='error',error_count=1,local_ok_count=0 WHERE product_key=?`, product.ProductKey)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := store.ReplaceLocalFiles(ctx, files, "unchanged")
	if err != nil || !changed {
		t.Fatalf("background scan must heal derived errors: changed=%v err=%v", changed, err)
	}
	year, err := store.Year(ctx, product.ProductKey, 2025)
	if err != nil {
		t.Fatal(err)
	}
	if got := findYearDay(t, year, day); got.Status != "complete" || got.ErrorCount != 0 || got.LocalOKCount != 1 {
		t.Fatalf("historical stale error not healed: %+v", got)
	}
	if changed, err := store.ReplaceLocalFiles(ctx, files, "unchanged"); err != nil || changed {
		t.Fatalf("unchanged healthy inventory should be a no-op: changed=%v err=%v", changed, err)
	}
}

func findYearDay(t *testing.T, response YearResponse, day string) YearDay {
	t.Helper()
	for _, item := range response.Days {
		if item.Day == day {
			return item
		}
	}
	t.Fatalf("day %s not found in %+v", day, response)
	return YearDay{}
}
