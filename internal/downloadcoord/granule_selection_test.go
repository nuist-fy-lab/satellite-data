package downloadcoord

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestGranuleIDSelectsOnlyMatchingGroup(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := "viirs:C3860041042-LAADS"
	if err := s.UpsertProducts(t.Context(), "viirs", []model.Product{{ProductKey: key, SourceID: "viirs"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	granule := func(id string) model.Granule {
		return model.Granule{ID: id, Start: start.Add(time.Minute), End: start.Add(2 * time.Minute), Assets: []model.Asset{{ID: id, Name: id + ".dat", URL: "https://example.test/" + id}}}
	}
	p := &coverageProvider{source: "viirs", granules: []model.Granule{granule("g1"), granule("g2")}}
	group, _, err := s.EnqueueDownloadGroup(t.Context(), "local", start, start.Add(time.Hour), []state.GroupProduct{{Source: "viirs", Provider: "viirs", Collection: key, Selection: downloadfilter.Selection{GranuleIDs: []string{"g2"}}}})
	if err != nil {
		t.Fatal(err)
	}
	client, mu, specs := fakeTransferClient(t)
	c, err := New(provider.NewRegistry(p), s, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.advanceDiscovery(t.Context(), group.Downloads[0]); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*specs) != 1 || (*specs)[0].GranuleID != "g2" {
		t.Fatalf("queued: %+v", *specs)
	}
}
