package httpapi

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/producttime"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

// Interactive queries stay bounded; persistent orders have no dependency on this limit.
func searchGranules(ctx context.Context, p provider.Provider, q model.Query, selection downloadfilter.Selection) ([]model.Granule, error) {
	if !q.End.After(q.Start) {
		return nil, errors.New("end must be after start")
	}
	if regionalGranuleProduct(p.Source().ID, q.CollectionID) && q.End.Sub(q.Start) > 10*time.Minute {
		return nil, errors.New("interactive window: 区域观测查询请缩小到10分钟，较长范围请提交下载任务")
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
			if seen[granule.ID] || !producttime.OwnsCoverage(granule.Start, granule.End, q.Start, q.Start, q.End) {
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
