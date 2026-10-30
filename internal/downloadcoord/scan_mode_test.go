package downloadcoord

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type modeProvider struct{ granules []model.Granule }

func (*modeProvider) Source() model.Source                                    { return model.Source{ID: "goes16"} }
func (*modeProvider) Collections(context.Context) ([]model.Collection, error) { return nil, nil }
func (p *modeProvider) Search(context.Context, model.Query) ([]model.Granule, error) {
	return p.granules, nil
}

func TestOrderMatchesEachActualGOESScanMode(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const key = "goes16:ABI-L2-CMIPF"
	if err := s.UpsertProducts(t.Context(), "goes16", []model.Product{{ProductKey: key, SourceID: "goes16", Cadence: "10m"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &modeProvider{}
	for _, tc := range []struct {
		mode   string
		minute int
	}{{"3", 15}, {"4", 5}, {"6", 10}} {
		p.granules = append(p.granules, model.Granule{ID: tc.mode, Start: start.Add(time.Duration(tc.minute)*time.Minute + 21*time.Second), End: start.Add(time.Duration(tc.minute+4) * time.Minute), Metadata: map[string]string{"scan_mode": tc.mode}, Assets: []model.Asset{{ID: tc.mode, Name: "scan-" + tc.mode + ".nc", URL: "https://example.test/" + tc.mode}}})
	}
	request, err := s.EnqueueSelectedDownloadRequestOwned(t.Context(), "local", "goes16", "goes", key, start, start.Add(time.Hour), downloadfilter.Selection{Times: []string{"00:05", "00:10", "00:15"}})
	if err != nil {
		t.Fatal(err)
	}
	client, mu, specs := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.advanceDiscovery(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*specs) != 3 {
		t.Fatalf("historical modes lost: %+v", *specs)
	}
}
