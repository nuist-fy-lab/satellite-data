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
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const catalogBase = "https://catalog.maap.eo.esa.int/catalogue"

type Provider struct {
	client *http.Client

	catalogMu   sync.Mutex
	catalog     []model.Collection
	catalogTime time.Time
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

type seriesCatalog struct {
	Features []struct {
		ID         string         `json:"id"`
		Properties map[string]any `json:"properties"`
	} `json:"features"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	p.catalogMu.Lock()
	defer p.catalogMu.Unlock()
	if len(p.catalog) > 0 && time.Since(p.catalogTime) < 6*time.Hour {
		return append([]model.Collection(nil), p.catalog...), nil
	}
	values := url.Values{
		"platform": {"EarthCARE"},
		"limit":    {"100"},
		"mode":     {"owc"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogBase+"/collections?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/geo+json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ESA MAAP collection catalogue returned HTTP %d", resp.StatusCode)
	}
	var payload seriesCatalog
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode ESA MAAP collection catalogue: %w", err)
	}
	out := make([]model.Collection, 0, 64)
	for _, feature := range payload.Features {
		seriesID := propertyString(feature.Properties, "identifier")
		title := propertyString(feature.Properties, "title")
		if seriesID == "" {
			continue
		}
		productTypes := earthcareProductTypes(feature.Properties)
		if len(productTypes) == 0 {
			out = append(out, model.Collection{
				ID:         "earthcare:" + seriesID,
				SourceID:   "earthcare",
				Satellite:  "EarthCARE",
				Instrument: "EarthCARE",
				Level:      earthcareSeriesLevel(seriesID, title),
				Product:    seriesID,
				Cadence:    "event",
				Metadata: map[string]string{
					"official_id":     seriesID,
					"stac_collection": seriesID,
					"title":           title,
					"version_info":    propertyString(feature.Properties, "versionInfo"),
					"access_class":    earthcareAccessClass(seriesID, title),
				},
			})
			continue
		}
		for _, productType := range productTypes {
			out = append(out, model.Collection{
				ID:         "earthcare:" + seriesID + ":" + productType,
				SourceID:   "earthcare",
				Satellite:  "EarthCARE",
				Instrument: earthcareInstrument(productType),
				Level:      earthcareProductLevel(productType),
				Product:    productType,
				Cadence:    "event",
				Metadata: map[string]string{
					"official_id":     productType,
					"stac_collection": seriesID,
					"product_type":    productType,
					"title":           title + " · " + productType,
					"series_title":    title,
					"version_info":    propertyString(feature.Properties, "versionInfo"),
					"access_class":    earthcareAccessClass(seriesID, title),
				},
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Metadata["stac_collection"] == out[j].Metadata["stac_collection"] {
			return out[i].Product < out[j].Product
		}
		return out[i].Metadata["stac_collection"] < out[j].Metadata["stac_collection"]
	})
	p.catalog = out
	p.catalogTime = time.Now()
	return append([]model.Collection(nil), out...), nil
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
	catalogKey, ok := strings.CutPrefix(q.CollectionID, "earthcare:")
	if !ok || !p.knownCollection(ctx, q.CollectionID) {
		return nil, provider.ErrUnsupportedCollection
	}
	parts := strings.SplitN(catalogKey, ":", 2)
	collectionID := parts[0]
	productType := ""
	if len(parts) == 2 {
		productType = parts[1]
	}

	values := url.Values{
		"collections": {collectionID},
		"datetime":    {q.Start.UTC().Format(time.RFC3339) + "/" + q.End.UTC().Format(time.RFC3339)},
		"limit":       {"1000"},
	}
	if productType != "" {
		values.Set("filter", "productType = '"+productType+"'")
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

func (p *Provider) knownCollection(ctx context.Context, id string) bool {
	collections, err := p.Collections(ctx)
	if err != nil {
		return false
	}
	for _, item := range collections {
		if item.ID == id {
			return true
		}
	}
	return false
}

func earthcareProductTypes(properties map[string]any) []string {
	raw, ok := properties["categories"].([]any)
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 16)
	for _, value := range raw {
		category, ok := value.(map[string]any)
		if !ok {
			continue
		}
		term := anyString(category["term"])
		if !strings.HasSuffix(term, "/productType") {
			continue
		}
		label := anyString(category["label"])
		if label == "" {
			continue
		}
		if _, exists := seen[label]; exists {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func earthcareInstrument(productType string) string {
	upper := strings.ToUpper(productType)
	switch {
	case strings.HasPrefix(upper, "ATL"):
		return "ATLID"
	case strings.HasPrefix(upper, "CPR"):
		return "CPR"
	case strings.HasPrefix(upper, "MSI"):
		return "MSI"
	case strings.HasPrefix(upper, "BBR"):
		return "BBR"
	default:
		return "Synergy"
	}
}

func earthcareProductLevel(productType string) string {
	upper := strings.ToUpper(productType)
	for _, level := range []string{"L0", "1A", "1B", "1C", "1D", "2A", "2B", "3"} {
		if strings.HasSuffix(upper, "_"+level) {
			if strings.HasPrefix(level, "L") {
				return level
			}
			return "L" + level
		}
	}
	return ""
}

func earthcareSeriesLevel(seriesID, title string) string {
	upper := strings.ToUpper(seriesID + " " + title)
	switch {
	case strings.Contains(upper, "L0") && strings.Contains(upper, "L1"):
		return "L0/L1"
	case strings.Contains(upper, "L1D"):
		return "L1D"
	case strings.Contains(upper, "L1"):
		return "L1"
	case strings.Contains(upper, "L2"):
		return "L2"
	case strings.Contains(upper, "ORBIT"):
		return "orbit"
	case strings.Contains(upper, "AUX"):
		return "aux"
	default:
		return ""
	}
}

func earthcareAccessClass(seriesID, title string) string {
	upper := strings.ToUpper(seriesID + " " + title)
	switch {
	case strings.Contains(upper, "INSTCHECKED"), strings.Contains(upper, "CAL/VAL"):
		return "calval"
	case strings.Contains(upper, "PRODUCTS_MAAP"), strings.Contains(upper, "COMMISSIONING"):
		return "commissioning"
	default:
		return "open"
	}
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
