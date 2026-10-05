package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

type queryProvider struct{ queries []model.Query }

func (*queryProvider) Source() model.Source                                    { return model.Source{ID: "goes18"} }
func (*queryProvider) Collections(context.Context) ([]model.Collection, error) { return nil, nil }
func (p *queryProvider) Search(_ context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	p.queries = append(p.queries, q)
	return []model.Granule{{ID: q.Start.Format(time.RFC3339), Start: q.Start}}, nil
}

func TestCentreSplitsAnInteractiveQueryWithoutClientBatching(t *testing.T) {
	p := &queryProvider{}
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	items, err := searchGranules(t.Context(), p, model.Query{CollectionID: "goes18:GLM-L2-LCFA", Start: first, End: first.AddDate(0, 0, 17)}, downloadfilter.Selection{})
	if err != nil || len(p.queries) != 3 || len(items) != 3 {
		t.Fatalf("centre query plan: calls=%d items=%d err=%v", len(p.queries), len(items), err)
	}
	for i := 1; i < len(p.queries); i++ {
		if !p.queries[i].Start.Equal(p.queries[i-1].End) {
			t.Fatal("query segments have a gap or overlap")
		}
	}
}
