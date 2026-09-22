package model

import "time"

type AccessTier string

const (
	TierAnonymous AccessTier = "anonymous"
	TierToken     AccessTier = "token"
	TierSpecial   AccessTier = "special"
)

type Capability string

const (
	CapabilityCatalog      Capability = "catalog"
	CapabilitySearch       Capability = "search"
	CapabilityDownload     Capability = "download"
	CapabilitySubscription Capability = "subscription"
)

type Source struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Tier         AccessTier   `json:"tier"`
	Method       string       `json:"method"`
	Capabilities []Capability `json:"capabilities"`
	Description  string       `json:"description,omitempty"`
}

type Collection struct {
	ID         string            `json:"id"`
	SourceID   string            `json:"source_id"`
	Satellite  string            `json:"satellite"`
	Instrument string            `json:"instrument"`
	Level      string            `json:"level,omitempty"`
	Product    string            `json:"product"`
	Cadence    string            `json:"cadence,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Product is the normalized, UI-facing official product catalogue entry.
// Its core field names intentionally follow nsmcdata's /api/products shape so
// the two dashboards can share the same filtering and family/variant model.
type Product struct {
	ProductKey    string            `json:"product_key"`
	SourceID      string            `json:"source_id"`
	OfficialID    string            `json:"official_id"`
	Satellite     string            `json:"satellite"`
	Instrument    string            `json:"instrument"`
	Level         string            `json:"level"`
	DataType      string            `json:"data_type"`
	AreaType      string            `json:"area_type"`
	DisplayName   string            `json:"display_name"`
	Resolution    string            `json:"resolution"`
	Cadence       string            `json:"cadence"`
	Version       string            `json:"version"`
	AccessTier    AccessTier        `json:"access_tier"`
	CatalogSource string            `json:"catalog_source"`
	ProductCount  int64             `json:"product_count,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type Asset struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	AuthRef   string            `json:"auth_ref,omitempty"`
	Size      int64             `json:"size,omitempty"`
	ETag      string            `json:"etag,omitempty"`
	MediaType string            `json:"media_type,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Granule struct {
	ID           string            `json:"id"`
	SourceID     string            `json:"source_id"`
	CollectionID string            `json:"collection_id"`
	Start        time.Time         `json:"start"`
	End          time.Time         `json:"end,omitempty"`
	Assets       []Asset           `json:"assets"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type Query struct {
	CollectionID string
	Start        time.Time
	End          time.Time
}
