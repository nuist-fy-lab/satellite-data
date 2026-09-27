package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

var ErrUnsupportedCollection = errors.New("unsupported collection")
var ErrOnDemand = errors.New("on-demand dataset: use the ERA5 request API with explicit variables, dates and area; there is no published granule listing")

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

func (r *Registry) Providers() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source().ID < out[j].Source().ID })
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

func ProductFromCollection(source model.Source, collection model.Collection) model.Product {
	metadata := collection.Metadata
	officialID := firstMetadata(metadata, "official_id", "collection_ref", "concept_id", "stac_collection", "thredds_product_version")
	if officialID == "" {
		officialID = collection.Product
	}
	displayName := firstMetadata(metadata, "title", "display_name")
	if displayName == "" {
		displayName = collection.Product
	}
	var productCount int64
	if value := firstMetadata(metadata, "product_count"); value != "" {
		productCount, _ = strconv.ParseInt(value, 10, 64)
	}
	return model.Product{
		ProductKey:    collection.ID,
		SourceID:      collection.SourceID,
		OfficialID:    officialID,
		Satellite:     collection.Satellite,
		Instrument:    collection.Instrument,
		Level:         collection.Level,
		DataType:      collection.Product,
		AreaType:      firstMetadata(metadata, "area_type", "coverage"),
		DisplayName:   displayName,
		Resolution:    firstMetadata(metadata, "spatial_resolution", "resolution"),
		Cadence:       collection.Cadence,
		Version:       firstMetadata(metadata, "version", "version_info"),
		AccessTier:    source.Tier,
		CatalogSource: catalogSource(source.ID),
		ProductCount:  productCount,
		Metadata:      copyMetadata(metadata),
	}
}

func firstMetadata(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := metadata[key]; value != "" {
			return value
		}
	}
	return ""
}

func copyMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func catalogSource(sourceID string) string {
	switch sourceID {
	case "mtg":
		return "EUMETSAT Data Store Browse API"
	case "modis", "viirs", "calipso":
		return "NASA Common Metadata Repository (CMR)"
	case "earthcare":
		return "ESA MAAP OpenSearch/STAC catalogue"
	case "cloudsat":
		return "CloudSat DPC THREDDS catalogue"
	case "era5":
		return "Copernicus Climate Data Store STAC catalogue"
	default:
		if len(sourceID) >= 4 && sourceID[:4] == "goes" {
			return "NOAA Open Data Dissemination (NODD) object catalogue"
		}
		if len(sourceID) >= 8 && sourceID[:8] == "himawari" {
			return "NOAA Open Data Dissemination (NODD) object catalogue"
		}
		return "official provider catalogue"
	}
}
