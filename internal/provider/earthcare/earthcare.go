package earthcare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const catalogBase = "https://catalog.maap.eo.esa.int/catalogue"

var collections = []struct {
	ID     string
	Level  string
	Access string
}{
	{"EarthCAREL1Validated_MAAP", "L1", "validated"},
	{"EarthCAREL2Validated_MAAP", "L2", "validated"},
	{"JAXAL2Validated_MAAP", "L2", "validated"},
	{"EarthCAREL1InstChecked_MAAP", "L1", "calval"},
	{"EarthCAREL2InstChecked_MAAP", "L2", "calval"},
	{"JAXAL2InstChecked_MAAP", "L2", "calval"},
	{"EarthCAREL0L1Products_MAAP", "L0/L1", "commissioning"},
	{"EarthCAREL2Products_MAAP", "L2", "commissioning"},
	{"EarthCAREXMETL1DProducts10_MAAP", "L1D", "validated"},
	{"JAXAL2Products_MAAP", "L2", "commissioning"},
	{"EarthCAREOrbitData_MAAP", "orbit", "open"},
	{"EarthCAREAuxiliary_MAAP", "aux", "open"},
}

type Provider struct {
	client *http.Client
}

func New(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Provider{client: client}
}

func (p *Provider) Source() model.Source {
	return model.Source{
		ID:     "earthcare",
		Name:   "EarthCARE",
		Tier:   model.TierToken,
		Method: "ESA MAAP STAC discovery + OIDC-protected HTTPS science assets",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
			model.CapabilitySubscription,
		},
		Description: "STAC discovery is public. Data assets use the ESA MAAP authentication scheme; validated, Cal/Val and commissioning collections retain their source access class.",
	}
}

func (p *Provider) Collections(context.Context) ([]model.Collection, error) {
	out := make([]model.Collection, 0, len(collections))
	for _, item := range collections {
		out = append(out, model.Collection{
			ID:         "earthcare:" + item.ID,
			SourceID:   "earthcare",
			Satellite:  "EarthCARE",
			Instrument: "multi-instrument",
			Level:      item.Level,
			Product:    item.ID,
			Cadence:    "event",
			Metadata: map[string]string{
				"stac_collection": item.ID,
				"access_class":    item.Access,
			},
		})
	}
	return out, nil
}

type stacPage struct {
	NumberMatched  int           `json:"numberMatched"`
	NumberReturned int           `json:"numberReturned"`
	Features       []stacFeature `json:"features"`
	Links          []stacLink    `json:"links"`
}

type stacFeature struct {
	ID         string                    `json:"id"`
	Collection string                    `json:"collection"`
	Properties map[string]any            `json:"properties"`
	Assets     map[string]map[string]any `json:"assets"`
}

type stacLink struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	collectionID, ok := strings.CutPrefix(q.CollectionID, "earthcare:")
	if !ok || !knownCollection(collectionID) {
		return nil, provider.ErrUnsupportedCollection
	}

	values := url.Values{
		"collections": {collectionID},
		"datetime":    {q.Start.UTC().Format(time.RFC3339) + "/" + q.End.UTC().Format(time.RFC3339)},
		"limit":       {"1000"},
	}
	nextURL := catalogBase + "/search?" + values.Encode()
	out := make([]model.Granule, 0, 1024)
	for pageNumber := 0; nextURL != "" && pageNumber < 1000; pageNumber++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/geo+json")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		var page stacPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("ESA MAAP STAC search returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode ESA MAAP STAC search: %w", decodeErr)
		}
		for _, feature := range page.Features {
			granule, ok := featureGranule(feature, q.CollectionID)
			if ok && granule.End.After(q.Start) && granule.Start.Before(q.End) {
				out = append(out, granule)
			}
		}
		nextURL = nextLink(page.Links)
		if page.NumberReturned == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func featureGranule(feature stacFeature, collectionID string) (model.Granule, bool) {
	start, ok := propertyTime(feature.Properties, "start_datetime", "datetime")
	if !ok {
		return model.Granule{}, false
	}
	end, ok := propertyTime(feature.Properties, "end_datetime")
	if !ok {
		end = start
	}
	assets := scienceAssets(feature.Assets)
	if len(assets) == 0 {
		return model.Granule{}, false
	}
	return model.Granule{
		ID:           "earthcare:" + feature.ID,
		SourceID:     "earthcare",
		CollectionID: collectionID,
		Start:        start,
		End:          end,
		Assets:       assets,
		Metadata: map[string]string{
			"stac_collection":  feature.Collection,
			"product_type":     propertyString(feature.Properties, "product:type"),
			"processing_level": propertyString(feature.Properties, "processing:level"),
			"version":          propertyString(feature.Properties, "version"),
			"absolute_orbit":   propertyNumberString(feature.Properties, "sat:absolute_orbit"),
		},
	}, true
}

func scienceAssets(raw map[string]map[string]any) []model.Asset {
	assets := make([]model.Asset, 0, len(raw))
	archives := make([]model.Asset, 0, 1)
	for key, item := range raw {
		href := anyString(item["href"])
		if !strings.HasPrefix(href, "https://") || !hasRole(item["roles"], "data") {
			continue
		}
		name := anyString(item["file:local_path"])
		if name == "" {
			name = anyString(item["title"])
		}
		if name == "" {
			name = path.Base(href)
		}
		asset := model.Asset{
			ID:        key,
			Name:      name,
			URL:       href,
			AuthRef:   "earthcare",
			Size:      anyInt64(item["file:size"]),
			MediaType: anyString(item["type"]),
		}
		if hasRole(item["roles"], "archive") {
			archives = append(archives, asset)
		} else {
			assets = append(assets, asset)
		}
	}
	if len(assets) == 0 {
		assets = append(assets, archives...)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets
}

func knownCollection(id string) bool {
	for _, item := range collections {
		if item.ID == id {
			return true
		}
	}
	return false
}

func nextLink(links []stacLink) string {
	for _, link := range links {
		if link.Rel == "next" && strings.HasPrefix(link.Href, "https://catalog.maap.eo.esa.int/") {
			return link.Href
		}
	}
	return ""
}

func propertyTime(properties map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if value := propertyString(properties, key); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err == nil {
				return parsed.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func propertyString(properties map[string]any, key string) string {
	return anyString(properties[key])
}

func propertyNumberString(properties map[string]any, key string) string {
	switch value := properties[key].(type) {
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	default:
		return anyString(value)
	}
}

func anyString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func anyInt64(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case json.Number:
		parsed, _ := number.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(number, 10, 64)
		return parsed
	default:
		return 0
	}
}

func hasRole(value any, role string) bool {
	roles, ok := value.([]any)
	if !ok {
		return false
	}
	for _, candidate := range roles {
		if anyString(candidate) == role {
			return true
		}
	}
	return false
}
