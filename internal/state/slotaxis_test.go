package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestDayFixedAxisPreservesActualAndNominal(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Satellite: "GOES-18", Instrument: "ABI", Level: "L1b", AreaType: "DISK", DataType: "ABI-L1b-RadF", Cadence: "10m"}
	if err := store.UpsertProducts(t.Context(), "goes18", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := "2023-06-01"
	var granules []model.Granule
	for i, second := range []int{80, 82, 79} {
		start := time.Date(2023, 6, 1, 0, i*10, 0, 0, time.UTC).Add(time.Duration(second) * time.Second)
		granules = append(granules, model.Granule{ID: start.Format(time.RFC3339), SourceID: "goes18", CollectionID: product.ProductKey, Start: start, Assets: []model.Asset{{Name: start.Format("1504") + ".nc", Size: 1}}})
	}
	if err := store.SaveOfficialBatch(t.Context(), WorkBatch{Product: product.ProductKey, Days: []string{day}}, granules); err != nil {
		t.Fatal(err)
	}
	response, err := store.Day(t.Context(), product.ProductKey, day)
	if err != nil {
		t.Fatal(err)
	}
	if response.TimelineMode != "grid" || response.MatchToleranceSeconds < 80 || len(response.Slots) != 3 || response.Slots[0].NominalSlot != "2023-06-01T00:00:00Z" || response.Slots[0].Slot == response.Slots[0].NominalSlot {
		t.Fatalf("day=%+v", response)
	}
	if err := store.SaveOfficialBatch(t.Context(), WorkBatch{Product: product.ProductKey, Days: []string{day}}, append(granules, model.Granule{ID: "off-grid", SourceID: "goes18", CollectionID: product.ProductKey, Start: time.Date(2023, 6, 1, 0, 34, 30, 0, time.UTC), Assets: []model.Asset{{Name: "off-grid.nc", Size: 1}}})); err != nil {
		t.Fatal(err)
	}
	response, err = store.Day(t.Context(), product.ProductKey, day)
	if err != nil || response.TimelineMode != "events" {
		t.Fatalf("off-grid day=%+v err=%v", response, err)
	}
}
