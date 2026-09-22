package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

var ErrUnsupportedCollection = errors.New("unsupported collection")

type Provider interface {
	Source() model.Source
	Collections(context.Context) ([]model.Collection, error)
	Search(context.Context, model.Query) ([]model.Granule, error)
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Source().ID] = p
}

func (r *Registry) Sources() []model.Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]model.Source, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Source())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Provider(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %q: %w", id, ErrUnsupportedCollection)
	}
	return p, nil
}
