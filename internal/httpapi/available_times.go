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
	if regionalGranuleProduct(source, key) {
		return true
	}
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

func regionalGranuleProduct(source, key string) bool {
	return (source == "himawari8" || source == "himawari9") && (key == source+":AHI-L1b-Japan" || key == source+":AHI-L1b-Target")
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
	regional := regionalGranuleProduct(source, key)
	if regional {
		clock := r.URL.Query().Get("time")
		at, err := time.Parse("15:04", clock)
		if err != nil || at.Format("15:04") != clock || at.Minute()%10 != 0 {
			writeError(w, http.StatusBadRequest, errors.New("regional query requires a UTC time aligned to 10 minutes"))
			return
		}
		start = start.Add(time.Duration(at.Hour())*time.Hour + time.Duration(at.Minute())*time.Minute)
	} else if r.URL.Query().Has("time") {
		writeError(w, http.StatusBadRequest, errors.New("time is only supported for regional queries"))
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
	timeout, duration := 30*time.Second, 24*time.Hour
	if regional {
		timeout, duration = 90*time.Second, 10*time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	end := start.Add(duration)
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
		if regional && (g.ID == "" || g.Start.IsZero()) {
			writeError(w, http.StatusBadGateway, errors.New("regional granule missing identity or start"))
			return
		}
		if g.ID == "" || g.Start.IsZero() || !g.Start.Before(end) {
			continue
		}
		if regional {
			if g.End.IsZero() || !g.End.After(g.Start) {
				writeError(w, http.StatusBadGateway, errors.New("regional granule missing coverage"))
				return
			}
			if !g.End.After(start) {
				continue
			}
		} else if g.Start.Before(start) {
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
