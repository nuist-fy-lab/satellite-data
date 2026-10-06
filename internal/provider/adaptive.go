package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

const (
	minQueryGap = 200 * time.Millisecond
	maxQueryGap = 5 * time.Second
	maxQueries  = 2
)

type queryFlow struct {
	mu     sync.Mutex
	wake   chan struct{}
	limit  int
	active int
	gap    time.Duration
	next   time.Time
	rate   float64
	streak int
}

func newQueryFlow() *queryFlow {
	return &queryFlow{wake: make(chan struct{}), limit: maxQueries, gap: 500 * time.Millisecond}
}

func (f *queryFlow) acquire(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.mu.Lock()
		wait := time.Until(f.next)
		if f.active < f.limit && wait <= 0 {
			f.active++
			f.next = time.Now().Add(f.gap)
			f.mu.Unlock()
			return nil
		}
		wake := f.wake
		f.mu.Unlock()
		if wait <= 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wake:
			}
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (f *queryFlow) finish(success, count bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
	if count {
		f.rate = f.rate * 0.8
		if !success {
			f.rate += 0.2
			f.streak = 0
			if f.rate >= 0.2 {
				f.limit = max(1, f.limit-1)
				f.gap = min(maxQueryGap, f.gap*2)
				if next := time.Now().Add(f.gap); next.After(f.next) {
					f.next = next
				}
			}
		} else {
			f.streak++
			if f.streak >= 8 && f.rate < 0.1 {
				f.limit = min(maxQueries, f.limit+1)
				f.gap = max(minQueryGap, f.gap*4/5)
				f.streak = 0
			}
		}
	}
	close(f.wake)
	f.wake = make(chan struct{})
}

type adaptiveProvider struct {
	Provider
	flow *queryFlow
}

func (p *adaptiveProvider) Collections(ctx context.Context) ([]model.Collection, error) {
	if err := p.flow.acquire(ctx); err != nil {
		return nil, err
	}
	items, err := p.Provider.Collections(ctx)
	p.flow.finish(err == nil, ctx.Err() == nil && !errors.Is(err, ErrUnsupportedCollection))
	if err == nil {
		items = append([]model.Collection(nil), items...)
		for i := range items {
			items[i] = NormalizeCollection(items[i])
		}
	}
	return items, err
}

func (p *adaptiveProvider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := p.flow.acquire(ctx); err != nil {
		return nil, err
	}
	items, err := p.Provider.Search(ctx, q)
	p.flow.finish(err == nil, ctx.Err() == nil && !errors.Is(err, ErrUnsupportedCollection) && !errors.Is(err, ErrOnDemand))
	return items, err
}
