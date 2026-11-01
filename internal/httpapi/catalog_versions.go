package httpapi

import (
	"sort"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// A version alternative is an independent official collection, never a
// replacement for the selected product's stable collection identity.
type collectionVersion struct {
	ProductKey string `json:"product_key"`
	Version    string `json:"version,omitempty"`
	OfficialID string `json:"official_id,omitempty"`
}

type productFamily struct {
	SourceID  string
	Satellite string
	DataType  string
}

// Build relationships from the current official catalogue. The same product
// name may have distinct collection IDs, versions and production periods.
// Neither local archive first/last-day nor version ordering proves official
// availability over a historical period.
func catalogVersionAlternatives(products []model.Product) map[string][]collectionVersion {
	families := make(map[productFamily][]collectionVersion)
	for _, p := range products {
		if p.ProductKey == "" || p.DataType == "" {
			continue
		}
		family := productFamily{p.SourceID, p.Satellite, p.DataType}
		families[family] = append(families[family], collectionVersion{
			ProductKey: p.ProductKey, Version: p.Version, OfficialID: p.OfficialID,
		})
	}
	result := make(map[string][]collectionVersion)
	for _, entries := range families {
		if len(entries) <= 1 {
			continue
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Version == entries[j].Version {
				return entries[i].ProductKey < entries[j].ProductKey
			}
			return entries[i].Version < entries[j].Version
		})
		for i, current := range entries {
			other := make([]collectionVersion, 0, len(entries)-1)
			for j, candidate := range entries {
				if i != j && candidate.ProductKey != current.ProductKey {
					other = append(other, candidate)
				}
			}
			if len(other) > 0 {
				result[current.ProductKey] = other
			}
		}
	}
	return result
}
