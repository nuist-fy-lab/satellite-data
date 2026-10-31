package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func availableGranuleProduct(source, key string) bool {
	if source == "viirs" {
		return key == "viirs:C3860041042-LAADS"
	}
	switch source {
	case "goes16", "goes17", "goes18", "goes19":
		for _, code := range []string{"ABI-L1b-RadF", "ABI-L1b-RadC", "ABI-L2-CMIPF", "ABI-L2-CMIPC", "ABI-L2-ACHAF", "ABI-L2-ACMF"} {
			if key == source+":"+code {
				return true
			}
		}
	}
	return false
}

func (s *Server) availableTimes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.orderUser(w, r); !ok {
		return
	}
	key, day := r.URL.Query().Get("product"), r.URL.Query().Get("date")
	start, err := time.Parse(time.DateOnly, day)
	if err != nil || start.Format(time.DateOnly) != day {
		writeError(w, http.StatusBadRequest, errors.New("invalid UTC date"))
		return
	}
	source, _, err := s.store.ProductSource(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("unknown product"))
		return
	}
	if !availableGranuleProduct(source, key) {
		writeError(w, http.StatusBadRequest, errors.New("granule listing unavailable for product"))
		return
	}
	select {
	case s.availableGate <- struct{}{}:
		defer func() { <-s.availableGate }()
	case <-r.Context().Done():
		return
	default:
		writeError(w, http.StatusTooManyRequests, errors.New("granule listing busy; retry later"))
		return
	}
	p, err := s.registry.Provider(source)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	end := start.Add(24 * time.Hour)
	granules, err := p.Search(ctx, model.Query{CollectionID: key, Start: start, End: end})
	if err != nil || ctx.Err() != nil {
		writeError(w, http.StatusBadGateway, errors.New("granule listing failed"))
		return
	}
	if len(granules) > 2000 {
		writeError(w, http.StatusBadGateway, errors.New("granule listing exceeds limit"))
		return
	}
	type item struct {
		ID    string     `json:"id"`
		Start time.Time  `json:"start"`
		End   *time.Time `json:"end,omitempty"`
	}
	items := make([]item, 0, len(granules))
	seen := make(map[string]bool)
	for _, g := range granules {
		if g.ID == "" || g.Start.IsZero() || g.Start.Before(start) || !g.Start.Before(end) {
			continue
		}
		if seen[g.ID] {
			writeError(w, http.StatusBadGateway, errors.New("duplicate granule ID"))
			return
		}
		seen[g.ID] = true
		entry := item{ID: g.ID, Start: g.Start.UTC()}
		if !g.End.IsZero() {
			at := g.End.UTC()
			entry.End = &at
		}
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Start.Equal(items[j].Start) {
			return items[i].ID < items[j].ID
		}
		return items[i].Start.Before(items[j].Start)
	})
	writeJSON(w, http.StatusOK, map[string]any{"product": key, "date": day, "granules": items})
}
