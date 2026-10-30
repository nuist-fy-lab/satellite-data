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

type coverageProvider struct{ granules []model.Granule }

func (*coverageProvider) Source() model.Source                                    { return model.Source{ID: "modis"} }
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
