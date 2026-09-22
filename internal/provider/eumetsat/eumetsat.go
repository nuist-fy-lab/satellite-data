package eumetsat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const (
	apiBase        = "https://api.eumetsat.int"
	searchEndpoint = apiBase + "/data/search-products/1.0.0/os"
	downloadBase   = apiBase + "/data/download/1.0.0/collections"
)

var mtgCollections = []model.Collection{
	{
		ID:         "mtg:EO:EUM:DAT:0662",
		SourceID:   "mtg",
		Satellite:  "Meteosat-12 (MTG-I1)",
		Instrument: "FCI",
		Level:      "L1c",
		Product:    "FCI Level 1c Normal Resolution Image Data (FDHSI)",
		Cadence:    "10m",
		Metadata: map[string]string{
			"collection_ref": "EO:EUM:DAT:0662",
			"coverage":       "full disk",
		},
	},
	{
		ID:         "mtg:EO:EUM:DAT:0665",
		SourceID:   "mtg",
		Satellite:  "Meteosat-12 (MTG-I1)",
		Instrument: "FCI",
		Level:      "L1c",
		Product:    "FCI Level 1c High Resolution Image Data (HRFI)",
		Cadence:    "10m",
		Metadata: map[string]string{
			"collection_ref": "EO:EUM:DAT:0665",
			"coverage":       "full disk",
		},
	},
}

type Provider struct {
	client *http.Client
}

func New(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &Provider{client: client}
}

func (p *Provider) Source() model.Source {
	return model.Source{
		ID:     "mtg",
		Name:   "Meteosat Third Generation",
		Tier:   model.TierToken,
		Method: "EUMETSAT Data Store OpenSearch + OAuth2 download; MQTT publication feed / EUMETCast Terrestrial for low-latency operation",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
			model.CapabilitySubscription,
		},
		Description: "Catalog/search are anonymously accessible; product download requires an EUMETSAT API token.",
	}
}

func (p *Provider) Collections(context.Context) ([]model.Collection, error) {
	out := make([]model.Collection, len(mtgCollections))
	copy(out, mtgCollections)
	return out, nil
}

type searchPage struct {
	TotalResults int `json:"totalResults"`
	Features     []struct {
		ID         string         `json:"id"`
		Properties map[string]any `json:"properties"`
	} `json:"features"`
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	collectionID, ok := strings.CutPrefix(q.CollectionID, "mtg:")
	if !ok || !knownCollection(collectionID) {
		return nil, provider.ErrUnsupportedCollection
	}

	const pageSize = 500
	out := make([]model.Granule, 0, 256)
	for startIndex := 0; ; startIndex += pageSize {
		values := url.Values{
			"format":  {"json"},
			"pi":      {collectionID},
			"si":      {strconv.Itoa(startIndex)},
			"c":       {strconv.Itoa(pageSize)},
			"set":     {"brief"},
			"dtstart": {q.Start.UTC().Format(time.RFC3339)},
			"dtend":   {q.End.UTC().Format(time.RFC3339)},
			"sort":    {"start,time,1"},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchEndpoint+"?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		var page searchPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("EUMETSAT OpenSearch returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode EUMETSAT OpenSearch: %w", decodeErr)
		}

		for _, feature := range page.Features {
			start, end, ok := parseDateProperty(feature.Properties["date"])
			if !ok {
				continue
			}
			productID := feature.ID
			downloadURL := downloadBase + "/" + url.PathEscape(collectionID) + "/products/" + url.PathEscape(productID)
			out = append(out, model.Granule{
				ID:           "mtg:" + productID,
				SourceID:     "mtg",
				CollectionID: q.CollectionID,
				Start:        start,
				End:          end,
				Assets: []model.Asset{
					{
						ID:        productID,
						Name:      productID + ".zip",
						URL:       downloadURL,
						AuthRef:   "eumetsat",
						MediaType: "application/zip",
						Metadata: map[string]string{
							"container": "EUMETSAT Data Store SIP",
						},
					},
				},
				Metadata: map[string]string{
					"collection_ref": collectionID,
					"publication":    stringProperty(feature.Properties["updated"]),
				},
			})
		}

		if startIndex+len(page.Features) >= page.TotalResults || len(page.Features) == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func knownCollection(id string) bool {
	for _, collection := range mtgCollections {
		if collection.Metadata["collection_ref"] == id {
			return true
		}
	}
	return false
}

func parseDateProperty(value any) (time.Time, time.Time, bool) {
	raw, ok := value.(string)
	if !ok || raw == "" {
		return time.Time{}, time.Time{}, false
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return time.Time{}, time.Time{}, false
	}
	start, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	end, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return start.UTC(), end.UTC(), true
}

func stringProperty(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
