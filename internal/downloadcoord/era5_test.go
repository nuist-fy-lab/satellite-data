package downloadcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type fakeCDS struct {
	submissions []era5.Submission
	submitError error
}

func (f *fakeCDS) Process(context.Context, string) (era5.Process, error) {
	inputs := map[string]json.RawMessage{}
	for _, key := range []string{"variable", "year", "month", "day", "time", "product_type", "area", "data_format", "download_format"} {
		inputs[key] = json.RawMessage(`{}`)
	}
	return era5.Process{ID: "reanalysis-era5-single-levels", Inputs: inputs}, nil
}
func (f *fakeCDS) Submit(_ context.Context, submission era5.Submission) (era5.Job, error) {
	f.submissions = append(f.submissions, submission)
	if f.submitError != nil {
		return era5.Job{}, f.submitError
	}
	return era5.Job{ID: fmt.Sprintf("job-%d", len(f.submissions)), Status: "accepted"}, nil
}
func (*fakeCDS) Job(_ context.Context, id string) (era5.Job, error) {
	return era5.Job{ID: id, Status: "successful"}, nil
}
func (*fakeCDS) Result(_ context.Context, id string) (model.Asset, error) {
	return model.Asset{ID: "era5:" + id, Name: id + ".nc", Size: 128, MediaType: "application/x-netcdf", URL: "https://object-store.os-api.cci2.ecmwf.int/cache/file?signature=private"}, nil
}

func fakeTransferClient(t *testing.T) (*downloader.Client, *sync.Mutex, *[]downloader.TaskSpec) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "downloader.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mu := &sync.Mutex{}
	specs := []downloader.TaskSpec{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []downloader.ProviderStatus{{SourceID: "era5", Enabled: true}}})
		case "/v1/tasks":
			var spec downloader.TaskSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				t.Error(err)
			}
			specs = append(specs, spec)
			_ = json.NewEncoder(w).Encode(map[string]any{"created": true, "task": downloader.Task{ID: int64(len(specs)), Key: spec.AssetID, Name: spec.Name}})
		case "/v1/tasks/status":
			var body struct {
				IDs []int64 `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			tasks := []downloader.Task{}
			for _, id := range body.IDs {
				tasks = append(tasks, downloader.Task{ID: id, State: "done"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return downloader.New(socket), mu, &specs
}

func TestOneERA5OrderSchedulesAndArchivesMultiplePartsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const key = "era5:reanalysis-era5-single-levels"
	if err := s.UpsertProducts(t.Context(), "era5", []model.Product{{ProductKey: key, SourceID: "era5", Instrument: "ERA5", Satellite: "ECMWF"}}); err != nil {
		t.Fatal(err)
	}
	order, err := s.EnqueueERA5Order(t.Context(), key, first, first.AddDate(0, 0, 2), map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)})
	if err != nil {
		t.Fatal(err)
	}
	transfers, mu, specs := fakeTransferClient(t)
	cds := &fakeCDS{}
	c, err := New(provider.NewRegistry(era5.New(nil)), s, transfers, cds)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.advanceDiscovery(t.Context(), order); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RecoverDownloadRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err = New(provider.NewRegistry(era5.New(nil)), s, transfers, cds)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		order, err = s.DownloadRequest(t.Context(), order.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.advanceDiscovery(t.Context(), order); err != nil {
			t.Fatal(err)
		}
	}
	order, err = s.DownloadRequest(t.Context(), order.ID)
	if err != nil || order.State != "transferring" || order.AssetsQueued != 2 || len(cds.submissions) != 2 {
		t.Fatalf("wrong lifecycle: %+v submissions=%d err=%v", order, len(cds.submissions), err)
	}
	if string(cds.submissions[0].Inputs["day"]) != `["01"]` || string(cds.submissions[1].Inputs["day"]) != `["02"]` {
		t.Fatal("centre did not split the logical date range")
	}
	mu.Lock()
	for _, spec := range *specs {
		if spec.SourceID != "era5" || strings.Contains(spec.URL, "signature") || !strings.HasSuffix(spec.URL, "/download") {
			t.Errorf("unsafe transfer specification: %+v", spec)
		}
	}
	mu.Unlock()
	if err := c.refreshRequest(t.Context(), order); err != nil {
		t.Fatal(err)
	}
	order, _ = s.DownloadRequest(t.Context(), order.ID)
	if order.State != "done" || order.FilesDone != 2 {
		t.Fatalf("archive completion not reflected: %+v", order)
	}
}

func TestUncertainERA5SubmissionIsNeverAutomaticallyRepeated(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	order, err := s.EnqueueERA5Order(t.Context(), "era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 1), map[string]json.RawMessage{"variable": json.RawMessage(`["2m_temperature"]`)})
	if err != nil {
		t.Fatal(err)
	}
	transfers, _, _ := fakeTransferClient(t)
	cds := &fakeCDS{submitError: era5.ErrSubmissionUncertain}
	c, _ := New(provider.NewRegistry(), s, transfers, cds)
	for range 2 {
		if err := c.advanceDiscovery(t.Context(), order); !errors.Is(err, era5.ErrSubmissionUncertain) {
			t.Fatalf("expected uncertainty, got %v", err)
		}
	}
	if len(cds.submissions) != 1 {
		t.Fatal("uncertain CDS POST was repeated")
	}
}
