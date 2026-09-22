package laads

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

const apiBase = "https://ladsweb.modaps.eosdis.nasa.gov/api/v2"

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
		Method: "NASA LAADS API v2 JSON/REST discovery + LAADS Download Token HTTPS transfer",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
			model.CapabilitySubscription,
		},
		Description: "Current LAADS API v2 path; avoids the retired LAADS Classic service. LAADS-specific download tokens are preferred for transfer performance.",
	}
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	// LAADS' API v2 product catalogue is large (~MiB) and can take many
	// seconds over the c2ln1 path. Fetch it once for both Terra and Aqua and
	// retain it for six hours rather than issuing two heavyweight requests on
	// every UI/API catalogue view.
	p.catalogMu.Lock()
	defer p.catalogMu.Unlock()
	if len(p.catalog) > 0 && time.Since(p.catalogTime) < 6*time.Hour {
		return append([]model.Collection(nil), p.catalog...), nil
	}
	collections, err := p.products(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(collections, func(i, j int) bool {
		if collections[i].Satellite == collections[j].Satellite {
			return collections[i].Product < collections[j].Product
		}
		return collections[i].Satellite < collections[j].Satellite
	})
	p.catalog = collections
	p.catalogTime = time.Now()
	return append([]model.Collection(nil), collections...), nil
}

type productInfo struct {
	Name               string `json:"name"`
	Title              string `json:"title"`
	ProcessingLevel    string `json:"processingLevel"`
	InstrumentPlatform string `json:"instrument:platform"`
	SpatialResolution  string `json:"spatialResolution"`
	TemporalResolution string `json:"temporalResolution"`
	DefaultCollection  any    `json:"defaultCollection"`
	DefaultArchiveSet  any    `json:"defaultArchiveSet"`
}

func (p *Provider) products(ctx context.Context) ([]model.Collection, error) {
	values := url.Values{
		"instruments": {"MODIS"},
		"formats":     {"json"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/measurements/products?"+values.Encode(), nil)
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
		return nil, fmt.Errorf("LAADS products returned HTTP %d", resp.StatusCode)
	}
	var raw map[string]productInfo
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode LAADS products: %w", err)
	}
	out := make([]model.Collection, 0, len(raw))
	for key, item := range raw {
		shortName := strings.TrimSpace(item.Name)
		if shortName == "" {
			shortName = strings.TrimSpace(key)
		}
		platform := modisPlatform(item.InstrumentPlatform)
		if shortName == "" || platform == "" {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = shortName
		}
		out = append(out, model.Collection{
			ID:         "modis:" + shortName,
			SourceID:   "modis",
			Satellite:  platform,
			Instrument: "MODIS",
			Level:      item.ProcessingLevel,
			Product:    shortName,
			Cadence:    "event",
			Metadata: map[string]string{
				"title":               title,
				"spatial_resolution":  item.SpatialResolution,
				"temporal_resolution": item.TemporalResolution,
				"default_collection":  fmt.Sprint(item.DefaultCollection),
				"default_archive_set": fmt.Sprint(item.DefaultArchiveSet),
				"version_strategy":    "LAADS API v2 current archive metadata",
			},
		})
	}
	return out, nil
}

func modisPlatform(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToUpper(value), "MODIS:") {
		return ""
	}
	platform := strings.TrimSpace(strings.TrimPrefix(value, "MODIS:"))
	switch strings.ToLower(platform) {
	case "terra":
		return "Terra"
	case "aqua":
		return "Aqua"
	default:
		return ""
	}
}

type detailsPage struct {
	Content []struct {
		ArchiveSet   any    `json:"archiveSets"`
		Checksum     string `json:"cksum"`
		Collection   string `json:"collections"`
		DownloadLink string `json:"downloadsLink"`
		FileID       any    `json:"fileId"`
		MD5          string `json:"md5sum"`
		Name         string `json:"name"`
		Product      string `json:"products"`
		Size         int64  `json:"size"`
		Start        string `json:"start"`
		Status       string `json:"status"`
	} `json:"content"`
	NextPageLink string `json:"nextPageLink"`
	Pages        int    `json:"n_pages"`
	Page         int    `json:"page"`
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	shortName, ok := strings.CutPrefix(q.CollectionID, "modis:")
	if !ok || shortName == "" || strings.ContainsAny(shortName, "/\\") {
		return nil, provider.ErrUnsupportedCollection
	}

	values := url.Values{
		"products":       {shortName},
		"temporalRanges": {formatRange(q.Start, q.End)},
		"formats":        {"json"},
	}
	nextURL := apiBase + "/content/details?" + values.Encode()
	out := make([]model.Granule, 0, 256)
	for pageNumber := 0; nextURL != "" && pageNumber < 1000; pageNumber++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		var page detailsPage
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("LAADS content/details returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode LAADS details: %w", decodeErr)
		}
		for _, item := range page.Content {
			if item.Name == "" || item.DownloadLink == "" || !strings.EqualFold(item.Status, "Online") {
				continue
			}
			start, err := time.ParseInLocation("2006-01-02 15:04:05", item.Start, time.UTC)
			if err != nil || start.Before(q.Start) || !start.Before(q.End) {
				continue
			}
			mediaType := "application/octet-stream"
			lower := strings.ToLower(item.Name)
			switch {
			case strings.HasSuffix(lower, ".nc"):
				mediaType = "application/x-netcdf"
			case strings.HasSuffix(lower, ".hdf") || strings.HasSuffix(lower, ".h5"):
				mediaType = "application/x-hdf"
			}
			fileID := fmt.Sprint(item.FileID)
			out = append(out, model.Granule{
				ID:           "modis:" + fileID,
				SourceID:     "modis",
				CollectionID: q.CollectionID,
				Start:        start,
				Assets: []model.Asset{
					{
						ID:        fileID,
						Name:      item.Name,
						URL:       item.DownloadLink,
						AuthRef:   "laads",
						Size:      item.Size,
						MediaType: mediaType,
						Metadata: map[string]string{
							"md5":      item.MD5,
							"checksum": item.Checksum,
						},
					},
				},
				Metadata: map[string]string{
					"archive_set": fmt.Sprint(item.ArchiveSet),
					"collection":  item.Collection,
					"product":     item.Product,
				},
			})
		}
		if page.NextPageLink == "" || page.NextPageLink == nextURL {
			break
		}
		nextURL = page.NextPageLink
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func formatRange(start, end time.Time) string {
	// LAADS API v2 accepts minute precision in UTC. Expand the upper edge to the
	// next minute so sub-minute caller ranges cannot accidentally omit a granule;
	// exact filtering is applied again after the API response.
	start = start.UTC()
	end = end.UTC()
	return start.Format("2006-01-02 15:04") + ".." + end.Add(time.Minute-1).Truncate(time.Minute).Format("2006-01-02 15:04")
}

func isMODISShortName(name, platform string) bool {
	upper := strings.ToUpper(name)
	switch strings.ToLower(platform) {
	case "terra":
		return strings.HasPrefix(upper, "MOD")
	case "aqua":
		return strings.HasPrefix(upper, "MYD")
	default:
		return false
	}
}
