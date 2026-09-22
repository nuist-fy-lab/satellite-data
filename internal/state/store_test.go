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
