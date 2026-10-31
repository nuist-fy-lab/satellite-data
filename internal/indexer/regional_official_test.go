package indexer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type regionalTestProvider struct{ queries chan model.Query }

func (p *regionalTestProvider) Source() model.Source { return model.Source{ID: "himawari9"} }
func (p *regionalTestProvider) Collections(context.Context) ([]model.Collection, error) {
	return nil, nil
}
func (p *regionalTestProvider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	p.queries <- q
	if q.Start.Minute() == 10 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, nil
}

func TestRegionalOfficialLoopUsesResumableTenMinuteWindows(t *testing.T) {
	ctx := t.Context()
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const key = "himawari9:AHI-L1b-Japan"
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.UpsertProducts(ctx, "himawari9", []model.Product{{ProductKey: key, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueRange(ctx, key, day, day.AddDate(0, 0, 1), 10); err != nil {
		t.Fatal(err)
	}
	p := &regionalTestProvider{queries: make(chan model.Query, 2)}
	i := New(provider.NewRegistry(p), s, t.TempDir(), "", "", "")
	runCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); i.officialLoop(runCtx, 1) }()
	defer func() { cancel(); <-finished }()
	for n := 0; n < 2; n++ {
		select {
		case q := <-p.queries:
			if !q.Start.Equal(day.Add(time.Duration(n)*10*time.Minute)) || q.End.Sub(q.Start) != 10*time.Minute {
				t.Fatalf("query %d: %+v", n, q)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
	}
	cancel()
	<-finished
	if err := s.RecoverOfficialWork(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := s.RegionalOfficialWindow(ctx, batch)
	if err != nil || !start.Equal(day.Add(10*time.Minute)) {
		t.Fatalf("resume %s %v", start, err)
	}
}
