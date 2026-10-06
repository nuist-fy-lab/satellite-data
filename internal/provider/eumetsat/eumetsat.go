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
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const (
	apiBase        = "https://api.eumetsat.int"
	searchEndpoint = apiBase + "/data/search-products/1.0.0/os"
	browseCatalog  = apiBase + "/data/browse/1.0.0/collections"
)

type Provider struct {
	client *http.Client

	catalogMu   sync.Mutex
	catalog     []model.Collection
	catalogTime time.Time
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

type browseCollectionList struct {
	Links []struct {
		Href             string  `json:"href"`
		Title            string  `json:"title"`
		DatasetTitle     string  `json:"datasetTitle"`
		Rel              string  `json:"rel"`
		NumberOfProducts float64 `json:"numberOfProducts"`
	} `json:"links"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	p.catalogMu.Lock()
	defer p.catalogMu.Unlock()
	if len(p.catalog) > 0 && time.Since(p.catalogTime) < 6*time.Hour {
		return append([]model.Collection(nil), p.catalog...), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, browseCatalog, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("EUMETSAT Browse API returned HTTP %d", resp.StatusCode)
	}
	var payload browseCollectionList
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode EUMETSAT collection catalogue: %w", err)
	}
	out := make([]model.Collection, 0, 32)
	for _, item := range payload.Links {
		title := strings.TrimSpace(item.DatasetTitle)
		officialID := strings.TrimSpace(item.Title)
		if item.Rel != "collection/details" || officialID == "" || !isMTGCollection(title) {
			continue
		}
		out = append(out, model.Collection{
			ID:         "mtg:" + officialID,
			SourceID:   "mtg",
			Satellite:  "MTG",
			Instrument: mtgInstrument(title),
			Level:      mtgLevel(title),
			Product:    officialID,
			Cadence:    mtgCadence(title),
			Metadata: map[string]string{
				"official_id":    officialID,
				"collection_ref": officialID,
				"title":          title,
				"product_count":  strconv.FormatInt(int64(item.NumberOfProducts), 10),
				"catalog_href":   item.Href,
				"area_type":      mtgArea(title),
				"product_type":   mtgProductType(title),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	p.catalog = out
	p.catalogTime = time.Now()
	return append([]model.Collection(nil), out...), nil
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
	if !ok || !p.knownCollection(ctx, collectionID) {
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
			"set":     {"full"},
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
			downloadURL, mediaType, ok := productDownloadLink(feature.Properties)
			if !ok {
				continue
			}
			if mediaType == "" {
				mediaType = "application/zip"
			}
			out = append(out, model.Granule{
				ID:           "mtg:" + productID,
				SourceID:     "mtg",
				CollectionID: q.CollectionID,
				Start:        start,
				End:          end,
				Assets: []model.Asset{
					{
						ID:        productID,
						Name:      productAssetName(productID, mediaType),
						URL:       downloadURL,
						AuthRef:   "eumetsat",
						MediaType: mediaType,
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

func productDownloadLink(properties map[string]any) (string, string, bool) {
	links, ok := properties["links"].(map[string]any)
	if !ok {
		return "", "", false
	}
	data, ok := links["data"].([]any)
	if !ok {
		return "", "", false
	}
	for _, raw := range data {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		href, _ := item["href"].(string)
		if !strings.HasPrefix(href, apiBase+"/data/download/") {
			continue
		}
		mediaType, _ := item["mediaType"].(string)
		return href, mediaType, true
	}
	return "", "", false
}

func productAssetName(productID, mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/zip", "application/x-zip-compressed":
		return productID + ".zip"
	default:
		return productID
	}
}

func (p *Provider) knownCollection(ctx context.Context, id string) bool {
	collections, err := p.Collections(ctx)
	if err != nil {
		return false
	}
	for _, collection := range collections {
		if collection.Metadata["collection_ref"] == id {
			return true
		}
	}
	return false
}

func isMTGCollection(title string) bool {
	return strings.Contains(strings.ToUpper(title), "MTG")
}

func mtgInstrument(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "FCI"):
		return "FCI"
	case strings.Contains(upper, "IRS"):
		return "IRS"
	case strings.Contains(upper, "LIGHTNING"), strings.Contains(upper, "LI "):
		return "LI"
	default:
		return "MTG"
	}
}

func mtgLevel(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "LEVEL 1C"):
		return "L1c"
	case strings.Contains(upper, "LEVEL 1B"):
		return "L1b"
	case strings.Contains(upper, "LEVEL 1"):
		return "L1"
	case strings.Contains(upper, "LEVEL 2"):
		return "L2"
	case strings.Contains(upper, "LEVEL 3"):
		return "L3"
	default:
		return ""
	}
}

func mtgCadence(title string) string {
	upper := strings.ToUpper(title)
	if strings.Contains(upper, "FCI LEVEL 1C") {
		return "10m"
	}
	return "event"
}

func mtgProductType(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "FCI LEVEL 1C") && strings.Contains(upper, "NORMAL RESOLUTION"):
		return "FCI-1C-RRAD-FDHSI-FD (normal resolution)"
	case strings.Contains(upper, "FCI LEVEL 1C") && strings.Contains(upper, "HIGH RESOLUTION"):
		return "FCI-1C-RRAD-HRFI-FD (high resolution)"
	case strings.Contains(upper, "IRS LEVEL 1") && strings.Contains(upper, "PRINCIPAL COMPONENTS"):
		return "IRS L1 - 300 principal components"
	case strings.Contains(upper, "IRS LEVEL 1") && strings.Contains(upper, "SPECTRAL SOUNDINGS"):
		return "IRS L1 - spectral soundings"
	default:
		return ""
	}
}

func mtgArea(title string) string {
	upper := strings.ToUpper(title)
	if strings.Contains(upper, "0 DEGREE") {
		return "DISK"
	}
	return ""
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
