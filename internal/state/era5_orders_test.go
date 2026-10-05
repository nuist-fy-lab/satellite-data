package state

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

func TestERA5OrderPersistsInputsAndPreSubmissionCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	inputs := map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)}
	order, err := s.EnqueueERA5Order(t.Context(), "era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 2), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimDownloadRequest(t.Context(), "era5"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := s.PrepareERA5Part(t.Context(), order, era5.Submission{CollectionID: order.CollectionID, Inputs: inputs}, first.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetERA5PartStage(t.Context(), order.ID, "submitting", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RecoverDownloadRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	part, err := s.ERA5OrderPart(t.Context(), order)
	if err != nil || part.Stage != "submitting" || part.JobID != "" {
		t.Fatalf("lost uncertainty checkpoint: %+v %v", part, err)
	}
	restored, err := s.ERA5Inputs(t.Context(), order.ID)
	if err != nil || string(restored["variable"]) != `["2m_temperature"]` {
		t.Fatal("lost selection inputs")
	}
	if err := s.PrepareERA5Part(t.Context(), order, part.Submission, part.End); err != nil {
		t.Fatal(err)
	}
	part, _ = s.ERA5OrderPart(t.Context(), order)
	if part.Stage != "submitting" {
		t.Fatal("preparing same part reset the submission checkpoint")
	}
}

func TestERA5OrderAndInputsCommitAtomically(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`CREATE TRIGGER reject_order_inputs BEFORE INSERT ON era5_order_inputs BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.EnqueueERA5Order(t.Context(), "era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 1), map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)}); err == nil {
		t.Fatal("failed input transaction succeeded")
	}
	items, err := s.DownloadRequests(t.Context(), 50)
	if err != nil || len(items) != 0 {
		t.Fatal("partial order remained after failed transaction")
	}
}

func TestERA5ResultManifestLinksOfficialJobToDownloaderTask(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	order, err := s.EnqueueERA5Order(t.Context(), "era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 1), map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordERA5Result(t.Context(), order.ID, 3693, "job-1", order.CollectionID, "result-1", "file.nc", 25171); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		taskID    int64
		job, name string
		size      int64
		want      bool
	}{
		{3693, "job-1", "file.nc", 25171, true},
		{3694, "job-1", "file.nc", 25171, false},
		{3693, "job-2", "file.nc", 25171, false},
		{3693, "job-1", "other.nc", 25171, false},
		{3693, "job-1", "file.nc", 1, false},
	} {
		match, err := s.ERA5ResultMatches(t.Context(), item.taskID, item.job, order.CollectionID, item.name, item.size)
		if err != nil || match != item.want {
			t.Errorf("task %d job %s file %s size %d => %t (%v), want %t", item.taskID, item.job, item.name, item.size, match, err, item.want)
		}
	}
}
