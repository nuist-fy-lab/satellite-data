package state

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

func TestERA5InventoryRequiresLocalFileAndCurrentVerification(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inputs := map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`), "time": json.RawMessage(`["00:00"]`), "area": json.RawMessage(`[32.25,118.5,32,118.75]`)}
	order, err := s.EnqueueERA5Order(ctx, "era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 2), inputs)
	if err != nil {
		t.Fatal(err)
	}
	part := ERA5Part{Start: first, End: first.AddDate(0, 0, 1), JobID: "job-one", Submission: era5.Submission{CollectionID: order.CollectionID, Inputs: inputs}}
	if err := s.RecordERA5Result(ctx, order.ID, 1, "job-one", order.CollectionID, "result-one", "data.nc", 100, part); err != nil {
		t.Fatal(err)
	}
	// A second remote result has no indexed local file and must not become stock.
	if err := s.RecordERA5Result(ctx, order.ID, 2, "remote-only", order.CollectionID, "remote-result", "remote.nc", 200); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordVerifiedArchiveFile(ctx, "/era5/data.nc", 100, 5, strings.Repeat("a", 64), "era5", order.CollectionID, "era5:job-one", "result-one"); err != nil {
		t.Fatal(err)
	}
	files := []LocalFile{{SourceID: "era5", Path: "/era5/data.nc", Filename: "data.nc", Size: 100, MtimeNS: 5, State: "ok"}, {SourceID: "era5", Path: "/era5/legacy.nc", Filename: "legacy.nc", Size: 50, MtimeNS: 6, State: "ok"}, {SourceID: "goes18", Path: "/goes/file.nc", Filename: "file.nc", Size: 70, MtimeNS: 7, State: "ok"}}
	if _, err := s.ReplaceLocalFiles(ctx, files, "first"); err != nil {
		t.Fatal(err)
	}
	inventory, err := s.ERA5Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Files) != 2 || inventory.Bytes != 150 || inventory.Unverified != 1 || inventory.ScannedAt == "" {
		t.Fatalf("wrong stock: %+v", inventory)
	}
	var verified ERA5InventoryFile
	for _, file := range inventory.Files {
		if file.Verified {
			verified = file
		}
	}
	if verified.Dataset != order.CollectionID || verified.Start != "2024-01-01T00:00:00Z" || verified.End != "2024-01-02T00:00:00Z" || len(verified.Variables) != 1 || verified.Variables[0] != "2m_temperature" || len(verified.Area) != 4 {
		t.Fatalf("wrong file slice: %+v", verified)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/era5/") || strings.Contains(string(encoded), "job-one") {
		t.Fatal("inventory leaked internal paths or job identifiers")
	}
	files[0].MtimeNS = 9
	if _, err := s.ReplaceLocalFiles(ctx, files, "changed"); err != nil {
		t.Fatal(err)
	}
	inventory, err = s.ERA5Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Unverified != 2 {
		t.Fatalf("stale verification accepted: %+v", inventory)
	}
	for _, file := range inventory.Files {
		if file.Verified || file.Start != "" || len(file.Variables) > 0 {
			t.Fatal("stale file inherited former content metadata")
		}
	}
	if _, err := s.ReplaceLocalFiles(ctx, files[1:], "removed"); err != nil {
		t.Fatal(err)
	}
	inventory, err = s.ERA5Inventory(ctx)
	if err != nil || len(inventory.Files) != 1 {
		t.Fatalf("removed file is still stock: %+v %v", inventory, err)
	}
}

func TestERA5InventoryRecoversOnlyMatchingLegacyJobSlice(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inputs := map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)}
	order, err := s.EnqueueERA5Order(ctx, "era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 2), inputs)
	if err != nil {
		t.Fatal(err)
	}
	submission := era5.Submission{CollectionID: order.CollectionID, Inputs: inputs}
	if err := s.PrepareERA5Part(ctx, order, submission, first.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetERA5PartStage(ctx, order.ID, "submitted", "legacy-job"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordERA5Result(ctx, order.ID, 1, "legacy-job", order.CollectionID, "asset", "file.nc", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordVerifiedArchiveFile(ctx, "/era5/file.nc", 100, 1, strings.Repeat("b", 64), "era5", order.CollectionID, "era5:legacy-job", "asset"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceLocalFiles(ctx, []LocalFile{{Path: "/era5/file.nc", Filename: "file.nc", Size: 100, MtimeNS: 1, State: "ok"}}, "legacy"); err != nil {
		t.Fatal(err)
	}
	inventory, err := s.ERA5Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Files[0].End != "2024-01-02T00:00:00Z" {
		t.Fatalf("legacy part lost or full order range invented: %+v", inventory)
	}
	if err := s.SetERA5PartStage(ctx, order.ID, "submitted", "different-job"); err != nil {
		t.Fatal(err)
	}
	inventory, err = s.ERA5Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Files[0].Start != "" || inventory.Files[0].End != "" {
		t.Fatal("different job part used as file coverage")
	}
}
