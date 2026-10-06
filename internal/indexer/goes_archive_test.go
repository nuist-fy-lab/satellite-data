package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestGOESArchiveAutomaticallyQueuesAndRecountsWithoutCalendarRequests(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive")
	if err := os.MkdirAll(archive, 0o750); err != nil {
		t.Fatal(err)
	}
	name := "OR_ABI-L1b-RadF-M6C06_G18_s20262650930215_e20262650939524_c20262650939553.nc"
	path := filepath.Join(archive, "goes18", "legacy-directory", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + ".part", filepath.Join(archive, "not-goes.nc")} {
		if err := os.WriteFile(file, []byte("payload"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	s, err := state.Open(filepath.Join(root, "index.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}
	if err := s.UpsertProducts(t.Context(), product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	i := New(nil, s, archive)
	i.scanLocal(t.Context())
	batch, err := s.ClaimBatch(t.Context(), 7)
	if err != nil || batch.Product != product.ProductKey || len(batch.Days) != 1 || batch.Days[0] != "2026-09-22" {
		t.Fatalf("filename-derived official lookup %+v (%v)", batch, err)
	}
	start := time.Date(2026, 9, 22, 9, 30, 21, 500_000_000, time.UTC)
	if err := s.SaveOfficialBatch(t.Context(), batch, []model.Granule{{ID: "slot", Start: start,
		Assets: []model.Asset{{Name: name, Size: 7}, {Name: "other-band.nc", Size: 7}}}}); err != nil {
		t.Fatal(err)
	}
	i.scanLocal(t.Context())
	day, err := s.Day(t.Context(), product.ProductKey, "2026-09-22")
	if err != nil || len(day.Slots) != 1 || len(day.Slots[0].Local) != 1 || day.Slots[0].Status != "partial" {
		t.Fatalf("partial bands must not imply all-band completion: %+v (%v)", day, err)
	}
	year, err := s.Year(t.Context(), product.ProductKey, 2026)
	if err != nil || len(year.Days) != 1 || year.Days[0].Status != "partial" || year.Days[0].LocalOKCount != 0 {
		t.Fatalf("partial band must appear in day summary without counting complete slots: %+v (%v)", year, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	i.scanLocal(t.Context())
	day, err = s.Day(t.Context(), product.ProductKey, "2026-09-22")
	if err != nil || len(day.Slots) != 1 || len(day.Slots[0].Local) != 0 {
		t.Fatalf("deleted file remained indexed: %+v (%v)", day, err)
	}
}
