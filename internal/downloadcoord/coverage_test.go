package downloadcoord

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type coverageProvider struct {
	source   string
	granules []model.Granule
}

func (p *coverageProvider) Source() model.Source {
	if p.source == "" {
		return model.Source{ID: "modis"}
	}
	return model.Source{ID: p.source}
}
func (*coverageProvider) Collections(context.Context) ([]model.Collection, error) { return nil, nil }
func (p *coverageProvider) Search(_ context.Context, q model.Query) ([]model.Granule, error) {
	var out []model.Granule
	for _, g := range p.granules {
		if g.Start.Before(q.End) && g.End.After(q.Start) {
			out = append(out, g)
		}
	}
	return out, nil
}

func TestIntervalProductsOwnOverlapsOnceAcrossChunks(t *testing.T) {
	for _, tc := range []struct{ source, provider, key string }{
		{"calipso", "calipso", "calipso:C3880523239-LARC_CLOUD"},
		{"cloudsat", "cloudsat", "cloudsat:3F-RMCP.R05_V0002"},
		{"goes18", "goes", "goes18:ABI-Flood-Day"},
		{"goes18", "goes", "goes18:ABI-Flood-Hourly"},
		{"modis", "modis", "modis:C3348093425-LAADS"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.UpsertProducts(t.Context(), tc.source, []model.Product{{ProductKey: tc.key, SourceID: tc.source, AreaType: "ORBIT"}}); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			step := discoveryChunk(tc.provider)
			granule := func(id string, from, until time.Time) model.Granule {
				return model.Granule{ID: id, Start: from, End: until, Assets: []model.Asset{{ID: id, Name: id + ".dat", URL: "https://example.test/" + id}}}
			}
			p := &coverageProvider{source: tc.source, granules: []model.Granule{
				granule("before", start.Add(-step), start),
				granule("overlap", start.Add(-step), start.Add(2*step)),
				granule("boundary", start.Add(step), start.Add(2*step)),
				granule("after", start.Add(2*step), start.Add(3*step)),
			}}
			request, err := s.EnqueueDownloadRequest(t.Context(), tc.source, tc.provider, tc.key, start, start.Add(2*step))
			if err != nil {
				t.Fatal(err)
			}
			client, mu, specs := fakeTransferClient(t)
			c, err := New(provider.NewRegistry(p), s, client)
			if err != nil {
				t.Fatal(err)
			}
			for request.Cursor.Before(request.End) {
				if err := c.advanceDiscovery(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				request, err = s.DownloadRequest(t.Context(), request.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(*specs) != 2 || (*specs)[0].GranuleID != "overlap" || (*specs)[1].GranuleID != "boundary" {
				t.Fatalf("coverage ownership: %+v", *specs)
			}
		})
	}
}

func TestCompositeIntersectingRequestStartIsScheduledOnceAcrossChunks(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	const key = "modis:composite"
	if err := s.UpsertProducts(t.Context(), "modis", []model.Product{{ProductKey: key, SourceID: "modis", Cadence: "8d"}}); err != nil {
		t.Fatal(err)
	}
	makeGranule := func(id string, from, until time.Time) model.Granule {
		return model.Granule{ID: id, Start: from, End: until, Assets: []model.Asset{{ID: id, Name: id + ".hdf", URL: "https://example.test/" + id}}}
	}
	p := &coverageProvider{granules: []model.Granule{
		makeGranule("old", start.AddDate(0, 0, -8), start),
		makeGranule("overlap", start.AddDate(0, 0, -4), start.AddDate(0, 0, 4)),
		makeGranule("new", start.AddDate(0, 0, 1), start.AddDate(0, 0, 9)),
		makeGranule("after", start.AddDate(0, 0, 2), start.AddDate(0, 0, 10)),
	}}
	request, err := s.EnqueueDownloadRequest(t.Context(), "modis", "modis", key, start, start.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	client, mu, specs := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	for request.Cursor.Before(request.End) {
		if err := c.advanceDiscovery(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		request, err = s.DownloadRequest(t.Context(), request.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*specs) != 2 || (*specs)[0].GranuleID != "overlap" || (*specs)[1].GranuleID != "new" {
		t.Fatalf("coverage files omitted or duplicated: %+v", *specs)
	}
}
