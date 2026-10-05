package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestRegistryRetainsConcreteProviderForSpecializedAPIs(t *testing.T) {
	// Generic discovery is paced while specialized provider methods keep their concrete type.
	r := NewRegistry(&stubProvider{})
	p, err := r.Provider("stub")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Underlying(p).(*stubProvider); !ok {
		t.Fatalf("unexpected underlying provider: %T", Underlying(p))
	}
}

type stubProvider struct{}

func (*stubProvider) Source() model.Source                                         { return model.Source{ID: "stub"} }
func (*stubProvider) Collections(context.Context) ([]model.Collection, error)      { return nil, nil }
func (*stubProvider) Search(context.Context, model.Query) ([]model.Granule, error) { return nil, nil }

func TestQueryFlowBacksOffAndRecoversWithinBounds(t *testing.T) {
	f := newQueryFlow()
	for range 8 {
		f.active++
		f.finish(false, true)
	}
	if f.limit != 1 || f.gap != maxQueryGap {
		t.Fatalf("failed requests did not reach safe limits: limit=%d gap=%s", f.limit, f.gap)
	}
	if time.Until(f.next) < 4*time.Second {
		t.Fatalf("failed query was not cooled down after completion: %s", time.Until(f.next))
	}
	for range 160 {
		f.active++
		f.finish(true, true)
	}
	if f.limit != maxQueries || f.gap != minQueryGap {
		t.Fatalf("successful requests did not recover: limit=%d gap=%s", f.limit, f.gap)
	}
}

func TestQueryFlowHonorsConcurrencyGapAndCancellation(t *testing.T) {
	f := newQueryFlow()
	f.gap = time.Hour
	if err := f.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second request bypassed pacing: %v", err)
	}
	f.finish(true, false)
	f.gap = 0
	f.next = time.Time{}
	if err := f.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third request exceeded query concurrency: %v", err)
	}
	f.finish(true, false)
	f.finish(true, false)
}
