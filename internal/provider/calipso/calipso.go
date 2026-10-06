package calipso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const cmrBase = "https://cmr.earthdata.nasa.gov/search"

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
		ID:     "calipso",
		Name:   "CALIPSO",
		Tier:   model.TierToken,
		Method: "NASA CMR discovery + ASDC Earthdata Login protected HTTPS transfer",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
		},
		Description: "CMR is used as the canonical catalog. Off-AWS downloads prefer the protected HTTPS asset over temporary same-region S3 credentials.",
	}
}

type collectionFeed struct {
	Feed struct {
		Entries []struct {
			ID                string   `json:"id"`
			ShortName         string   `json:"short_name"`
			VersionID         string   `json:"version_id"`
			DatasetID         string   `json:"dataset_id"`
			Summary           string   `json:"summary"`
			TimeStart         string   `json:"time_start"`
			TimeEnd           string   `json:"time_end"`
			ProcessingLevelID string   `json:"processing_level_id"`
			OnlineAccess      bool     `json:"online_access_flag"`
			Platforms         []string `json:"platforms"`
		} `json:"entry"`
	} `json:"feed"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	values := url.Values{
		"provider":  {"LARC_CLOUD"},
		"keyword":   {"CALIPSO"},
		"page_size": {"200"},
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
		return nil, fmt.Errorf("CMR collection search returned HTTP %d", resp.StatusCode)
	}
	var payload collectionFeed
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode CMR collection search: %w", err)
	}
	out := make([]model.Collection, 0, len(payload.Feed.Entries))
	for _, item := range payload.Feed.Entries {
		if !item.OnlineAccess || item.ID == "" || item.ShortName == "" || !containsFold(item.Platforms, "CALIPSO") {
			continue
		}
		out = append(out, model.Collection{
			ID:         "calipso:" + item.ID,
			SourceID:   "calipso",
			Satellite:  "CALIPSO",
			Instrument: calipsoInstrument(item.ShortName),
			Level:      calipsoLevel(item.ProcessingLevelID),
			Product:    item.ShortName,
			Cadence:    "event",
			Metadata: map[string]string{
				"concept_id":  item.ID,
				"version":     item.VersionID,
				"title":       item.DatasetID,
				"description": item.Summary,
				"time_start":  item.TimeStart,
				"time_end":    item.TimeEnd,
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	return out, nil
}

func calipsoLevel(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "L") {
		return value
	}
	return "L" + value
}

type granuleFeed struct {
	Feed struct {
		Entries []cmrGranule `json:"entry"`
	} `json:"feed"`
}

type cmrGranule struct {
	ID                string    `json:"id"`
	ProducerGranuleID string    `json:"producer_granule_id"`
	TimeStart         string    `json:"time_start"`
	TimeEnd           string    `json:"time_end"`
	GranuleSize       string    `json:"granule_size"`
	DayNightFlag      string    `json:"day_night_flag"`
	Links             []cmrLink `json:"links"`
}

type cmrLink struct {
	Rel       string `json:"rel"`
	Title     string `json:"title"`
	Href      string `json:"href"`
	Inherited bool   `json:"inherited"`
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	conceptID, ok := strings.CutPrefix(q.CollectionID, "calipso:")
	if !ok || conceptID == "" || strings.ContainsAny(conceptID, "/\\") {
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
	out := make([]model.Granule, 0, 256)
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
			return nil, fmt.Errorf("CMR granule search returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode CMR granule search: %w", decodeErr)
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
	return out, nil
}

func cmrGranuleModel(item cmrGranule, collectionID string) (model.Granule, bool) {
	start, err := time.Parse(time.RFC3339Nano, item.TimeStart)
	if err != nil {
		return model.Granule{}, false
	}
	end, err := time.Parse(time.RFC3339Nano, item.TimeEnd)
	if err != nil {
		end = start
	}
	link := preferredHTTPSLink(item.Links)
	if link == "" {
		return model.Granule{}, false
	}
	name := item.ProducerGranuleID
	if name == "" {
		name = path.Base(link)
	}
	return model.Granule{
		ID:           "calipso:" + item.ID,
		SourceID:     "calipso",
		CollectionID: collectionID,
		Start:        start.UTC(),
		End:          end.UTC(),
		Assets: []model.Asset{
			{
				ID:        item.ID,
				Name:      name,
				URL:       link,
				AuthRef:   "earthdata",
				MediaType: mediaTypeForName(name),
			},
		},
		Metadata: map[string]string{
			"day_night":    item.DayNightFlag,
			"granule_size": item.GranuleSize,
		},
	}, true
}

func preferredHTTPSLink(links []cmrLink) string {
	for _, link := range links {
		if link.Inherited || !strings.HasPrefix(link.Href, "https://data.asdc.earthdata.nasa.gov/") {
			continue
		}
		if strings.Contains(link.Rel, "/data#") {
			return link.Href
		}
	}
	return ""
}

func mediaTypeForName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".hdf") || strings.HasSuffix(lower, ".h5"):
		return "application/x-hdf"
	case strings.HasSuffix(lower, ".nc"):
		return "application/x-netcdf"
	default:
		return "application/octet-stream"
	}
}

func calipsoInstrument(shortName string) string {
	upper := strings.ToUpper(shortName)
	switch {
	case strings.HasPrefix(upper, "CAL_WFC_"):
		return "WFC"
	case strings.Contains(upper, "LID"):
		return "CALIOP"
	case strings.Contains(upper, "IIR"):
		return "IIR"
	case strings.Contains(upper, "WFC"):
		return "WFC"
	default:
		return "CALIPSO"
	}
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func setCMRHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Client-Id", "nuist-fy-lab-satellite-data")
}
