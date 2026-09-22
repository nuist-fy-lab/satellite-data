package laads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const cmrBase = "https://cmr.earthdata.nasa.gov/search"

var resolutionPattern = regexp.MustCompile(`(?i)\b(?:250|500|1000)\s*m\b|\b(?:1|5|10)\s*km\b|\b(?:0\.05|0\.1|0\.5|1)\s*(?:degree|deg)\b`)

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
		ID:     "modis",
		Name:   "MODIS Terra / Aqua",
		Tier:   model.TierToken,
		Method: "NASA CMR canonical collection/granule catalogue + LAADS Earthdata Cloud HTTPS transfer",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
			model.CapabilitySubscription,
		},
		Description: "CMR is the canonical MODIS catalogue for the LAADS provider. Science assets are selected from LAADS Earthdata Cloud HTTPS links.",
	}
}

type collectionFeed struct {
	Feed struct {
		Entries []struct {
			ID                string   `json:"id"`
			ShortName         string   `json:"short_name"`
			VersionID         string   `json:"version_id"`
			DatasetID         string   `json:"dataset_id"`
			ProcessingLevelID string   `json:"processing_level_id"`
			TimeStart         string   `json:"time_start"`
			TimeEnd           string   `json:"time_end"`
			CloudHosted       bool     `json:"cloud_hosted"`
			Platforms         []string `json:"platforms"`
		} `json:"entry"`
	} `json:"feed"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	p.catalogMu.Lock()
	defer p.catalogMu.Unlock()
	if len(p.catalog) > 0 && time.Since(p.catalogTime) < 6*time.Hour {
		return append([]model.Collection(nil), p.catalog...), nil
	}
	values := url.Values{
		"provider":   {"LAADS"},
		"instrument": {"MODIS"},
		"page_size":  {"2000"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmrBase+"/collections.json?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setCMRHeaders(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("CMR MODIS collection search returned HTTP %d", resp.StatusCode)
	}
	var payload collectionFeed
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode CMR MODIS collection search: %w", err)
	}
	out := make([]model.Collection, 0, len(payload.Feed.Entries))
	for _, item := range payload.Feed.Entries {
		if item.ID == "" || item.ShortName == "" {
			continue
		}
		satellite := modisSatellite(item.Platforms)
		if satellite == "" {
			continue
		}
		out = append(out, model.Collection{
			ID:         "modis:" + item.ID,
			SourceID:   "modis",
			Satellite:  satellite,
			Instrument: "MODIS",
			Level:      normalizeLevel(item.ProcessingLevelID),
			Product:    item.ShortName,
			Cadence:    modisCadence(item.DatasetID),
			Metadata: map[string]string{
				"official_id":        item.ID,
				"concept_id":         item.ID,
				"title":              item.DatasetID,
				"version":            item.VersionID,
				"spatial_resolution": modisResolution(item.DatasetID),
				"time_start":         item.TimeStart,
				"time_end":           item.TimeEnd,
				"cloud_hosted":       strconv.FormatBool(item.CloudHosted),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Satellite != out[j].Satellite {
			return out[i].Satellite < out[j].Satellite
		}
		if out[i].Product != out[j].Product {
			return out[i].Product < out[j].Product
		}
		return out[i].Metadata["version"] < out[j].Metadata["version"]
	})
	p.catalog = out
	p.catalogTime = time.Now()
	return append([]model.Collection(nil), out...), nil
}

type granuleFeed struct {
	Feed struct {
		Entries []cmrGranule `json:"entry"`
	} `json:"feed"`
}

type cmrGranule struct {
	ID                  string    `json:"id"`
	CollectionConceptID string    `json:"collection_concept_id"`
	ProducerGranuleID   string    `json:"producer_granule_id"`
	TimeStart           string    `json:"time_start"`
	TimeEnd             string    `json:"time_end"`
	GranuleSize         string    `json:"granule_size"`
	DayNightFlag        string    `json:"day_night_flag"`
	Links               []cmrLink `json:"links"`
}

type cmrLink struct {
	Rel       string `json:"rel"`
	Type      string `json:"type"`
	Href      string `json:"href"`
	Title     string `json:"title"`
	Inherited bool   `json:"inherited"`
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	conceptID, ok := strings.CutPrefix(q.CollectionID, "modis:")
	if !ok || conceptID == "" || strings.ContainsAny(conceptID, "/\\") || !p.knownCollection(ctx, q.CollectionID) {
		return nil, provider.ErrUnsupportedCollection
	}
	values := url.Values{
		"collection_concept_id": {conceptID},
		"temporal":              {q.Start.UTC().Format(time.RFC3339) + "," + q.End.UTC().Format(time.RFC3339)},
		"page_size":             {"2000"},
		"sort_key[]":            {"start_date"},
	}
	endpoint := cmrBase + "/granules.json?" + values.Encode()
	searchAfter := ""
	out := make([]model.Granule, 0, 512)
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		setCMRHeaders(req)
		if searchAfter != "" {
			req.Header.Set("CMR-Search-After", searchAfter)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		var payload granuleFeed
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		nextSearchAfter := resp.Header.Get("CMR-Search-After")
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("CMR MODIS granule search returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode CMR MODIS granule search: %w", decodeErr)
		}
		for _, item := range payload.Feed.Entries {
			granule, ok := cmrGranuleModel(item, q.CollectionID)
			if ok && granule.End.After(q.Start) && granule.Start.Before(q.End) {
				out = append(out, granule)
			}
		}
		if len(payload.Feed.Entries) == 0 || nextSearchAfter == "" || nextSearchAfter == searchAfter {
			break
		}
		searchAfter = nextSearchAfter
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func (p *Provider) knownCollection(ctx context.Context, id string) bool {
	collections, err := p.Collections(ctx)
	if err != nil {
		return false
	}
	for _, collection := range collections {
		if collection.ID == id {
			return true
		}
	}
	return false
}

func cmrGranuleModel(item cmrGranule, collectionID string) (model.Granule, bool) {
	start, err := time.Parse(time.RFC3339Nano, item.TimeStart)
	if err != nil {
		return model.Granule{}, false
	}
	end := start
	if item.TimeEnd != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, item.TimeEnd); err == nil {
			end = parsed
		}
	}
	link, ok := modisDataLink(item.Links)
	if !ok {
		return model.Granule{}, false
	}
	name := item.ProducerGranuleID
	if name == "" {
		name = path.Base(link.Href)
	}
	return model.Granule{
		ID:           "modis:" + item.ID,
		SourceID:     "modis",
		CollectionID: collectionID,
		Start:        start.UTC(),
		End:          end.UTC(),
		Assets: []model.Asset{
			{
				ID:        item.ID,
				Name:      name,
				URL:       link.Href,
				AuthRef:   "laads",
				Size:      parseMiB(item.GranuleSize),
				MediaType: modisMediaType(link.Type, name),
			},
		},
		Metadata: map[string]string{
			"concept_id":            item.ID,
			"collection_concept_id": item.CollectionConceptID,
			"day_night":             item.DayNightFlag,
		},
	}, true
}

func modisDataLink(links []cmrLink) (cmrLink, bool) {
	for _, link := range links {
		if link.Inherited || !strings.Contains(link.Rel, "/data#") {
			continue
		}
		if strings.HasPrefix(link.Href, "https://data.laadsdaac.earthdatacloud.nasa.gov/") {
			return link, true
		}
	}
	for _, link := range links {
		if link.Inherited || !strings.Contains(link.Rel, "/data#") || !strings.HasPrefix(link.Href, "https://") {
			continue
		}
		lower := strings.ToLower(link.Href)
		if !strings.HasSuffix(lower, ".met") && !strings.Contains(lower, "opendap") {
			return link, true
		}
	}
	return cmrLink{}, false
}

func modisSatellite(platforms []string) string {
	hasTerra, hasAqua := false, false
	for _, platform := range platforms {
		switch strings.ToLower(strings.TrimSpace(platform)) {
		case "terra":
			hasTerra = true
		case "aqua":
			hasAqua = true
		}
	}
	switch {
	case hasTerra && hasAqua:
		return "Terra / Aqua"
	case hasTerra:
		return "Terra"
	case hasAqua:
		return "Aqua"
	default:
		return ""
	}
}

func normalizeLevel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	upper := strings.ToUpper(value)
	if strings.HasPrefix(upper, "L") {
		return value
	}
	return "L" + value
}

func modisCadence(title string) string {
	upper := strings.ToUpper(title)
	switch {
	case strings.Contains(upper, "5-MIN"), strings.Contains(upper, "5 MIN"):
		return "5m"
	case strings.Contains(upper, "DAILY"):
		return "1d"
	case strings.Contains(upper, "8-DAY"), strings.Contains(upper, "8 DAY"):
		return "8d"
	case strings.Contains(upper, "MONTHLY"):
		return "1mo"
	default:
		return "event"
	}
}

func modisResolution(title string) string {
	matches := resolutionPattern.FindAllString(title, -1)
	if len(matches) == 0 {
		return ""
	}
	seen := map[string]struct{}{}
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		match = strings.ReplaceAll(strings.ToLower(match), " ", "")
		match = strings.ReplaceAll(match, "degree", "deg")
		if _, exists := seen[match]; exists {
			continue
		}
		seen[match] = struct{}{}
		values = append(values, match)
	}
	return strings.Join(values, "/")
}

func modisMediaType(linkType, name string) string {
	if linkType != "" {
		return linkType
	}
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".nc"):
		return "application/x-netcdf"
	case strings.HasSuffix(lower, ".hdf"), strings.HasSuffix(lower, ".h5"):
		return "application/x-hdf"
	default:
		return "application/octet-stream"
	}
}

func parseMiB(value string) int64 {
	size, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || size <= 0 {
		return 0
	}
	return int64(size * 1024 * 1024)
}

func setCMRHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Client-Id", "nuist-fy-lab-satellite-data")
}
