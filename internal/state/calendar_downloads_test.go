package state

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestCalendarActiveDownloadIntentUsesExactSelectedTimesForEverySource(t *testing.T) {
	for _, source := range []string{"modis", "viirs", "goes18", "himawari9", "earthcare", "mtg", "calipso", "cloudsat"} {
		t.Run(source, func(t *testing.T) {
			ctx := t.Context()
			store, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			product := model.Product{ProductKey: source + ":sample", SourceID: source, Cadence: "5m"}
			if err := store.UpsertProducts(ctx, source, []model.Product{product}); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2023, 1, 16, 0, 0, 0, 0, time.UTC)
			assets := make([]model.Granule, 0, 3)
			for i := 0; i < 3; i++ {
				when := at.Add(time.Duration(i*5) * time.Minute)
				assets = append(assets, model.Granule{ID: fmt.Sprintf("g%d", i), Start: when,
					Assets: []model.Asset{{Name: fmt.Sprintf("file%d.hdf", i), Size: 100}}})
			}
			if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2023-01-16"}}, assets); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReplaceLocalFiles(ctx, []LocalFile{{Path: "/archive/file0.hdf", Filename: "file0.hdf", Size: 100, MtimeNS: 1}}, "complete-first"); err != nil {
				t.Fatal(err)
			}
			order, err := store.EnqueueSelectedDownloadRequest(ctx, source, source, product.ProductKey, at, at.AddDate(0, 0, 1), downloadfilter.Selection{Times: []string{"00:00", "00:05"}})
			if err != nil {
				t.Fatal(err)
			}
			year, err := store.Year(ctx, product.ProductKey, 2023)
			if err != nil {
				t.Fatal(err)
			}
			day := findYearDay(t, year, "2023-01-16")
			if day.Status != "processing" || day.DownloadCount != 2 {
				t.Fatalf("active download %s: %+v", source, day)
			}
			detail, err := store.Day(ctx, product.ProductKey, "2023-01-16")
			if err != nil {
				t.Fatal(err)
			}
			if got := []string{detail.Slots[0].Status, detail.Slots[1].Status, detail.Slots[2].Status}; got[0] != "complete" || got[1] != "processing" || got[2] != "missing" {
				t.Fatalf("exact selection: %v", got)
			}
			if _, err := store.writer.ExecContext(ctx, `UPDATE download_requests SET state='done' WHERE id=?`, order.ID); err != nil {
				t.Fatal(err)
			}
			year, err = store.Year(ctx, product.ProductKey, 2023)
			if err != nil {
				t.Fatal(err)
			}
			if got := findYearDay(t, year, "2023-01-16"); got.Status == "processing" || got.DownloadCount != 0 {
				t.Fatalf("finished order stayed yellow: %+v", got)
			}
			detail, err = store.Day(ctx, product.ProductKey, "2023-01-16")
			if err != nil || detail.Slots[1].Status != "missing" {
				t.Fatalf("finished order slot=%+v err=%v", detail.Slots, err)
			}
		})
	}
}

func TestBandDownloadMarksOnlySelectedBandAndTime(t *testing.T) {
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
	at := time.Date(2023, 6, 14, 0, 0, 21, 0, time.UTC)
	makeGranule := func(minute int) model.Granule {
		var files []model.Asset
		for _, band := range []int{1, 6} {
			files = append(files, model.Asset{Name: fmt.Sprintf("OR_ABI-L1b-RadF-M6C%02d_G18_%d.nc", band, minute), Size: 1})
		}
		return model.Granule{ID: fmt.Sprint(minute), Start: at.Add(time.Duration(minute) * time.Minute), Assets: files}
	}
	if err := store.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey, Days: []string{"2023-06-14"}}, []model.Granule{makeGranule(0), makeGranule(10)}); err != nil {
		t.Fatal(err)
	}
	order, err := store.EnqueueSelectedDownloadRequest(ctx, "goes18", "goes", product.ProductKey, dayFloor(at), dayFloor(at).AddDate(0, 0, 1), downloadfilter.Selection{Band: 6, Times: []string{"00:00"}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(expected bool) {
		t.Helper()
		day, err := store.BandCoverageDay(ctx, product.ProductKey, "2023-06-14")
		if err != nil {
			t.Fatal(err)
		}
		for _, cell := range day.Cells {
			processing := cell[5] > 0
			want := expected && cell[0] == 0 && cell[1] == 6
			if processing != want {
				t.Fatalf("processing cell %+v want %v", cell, want)
			}
		}
	}
	check(true)
	if _, err := store.writer.ExecContext(ctx, `UPDATE download_requests SET state='done' WHERE id=?`, order.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
}
