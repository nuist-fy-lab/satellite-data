package httpapi

import (
	"context"
	"errors"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"sort"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

// Interactive queries stay bounded; persistent orders have no dependency on this limit.
func searchGranules(ctx context.Context, p provider.Provider, q model.Query, selection downloadfilter.Selection) ([]model.Granule, error) {
	if !q.End.After(q.Start) {
		return nil, errors.New("end must be after start")
	}
	if q.End.Sub(q.Start) > 31*24*time.Hour {
		return nil, errors.New("interactive query window exceeds 31 days; submit one persistent download order for longer ranges")
	}
	items := []model.Granule{}
	seen := make(map[string]bool)
	for cursor := q.Start; cursor.Before(q.End); {
		next := cursor.Add(provider.MaxInteractiveSearchWindow)
		if next.After(q.End) {
			next = q.End
		}
		chunk := q
		chunk.Start, chunk.End = cursor, next
		granules, err := p.Search(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for _, granule := range granules {
			if seen[granule.ID] || granule.Start.Before(q.Start) || !granule.Start.Before(q.End) {
				continue
			}
			if selection.Band != 0 {
				assets := []model.Asset{}
				for _, asset := range granule.Assets {
					if selection.MatchesAsset(p.Source().ID, q.CollectionID, asset.Name) {
						assets = append(assets, asset)
					}
				}
				if len(assets) == 0 {
					continue
				}
				granule.Assets = assets
			}
			seen[granule.ID] = true
			items = append(items, granule)
		}
		cursor = next
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Start.Before(items[j].Start) })
	return items, nil
}
