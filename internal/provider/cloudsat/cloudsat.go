package cloudsat

import (
	"context"
	"encoding/xml"
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

const (
	catalogRoot = "https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data"
	fileRoot    = "https://dpc-thredds.cira.colostate.edu/thredds/fileServer/"
)

type Provider struct {
	client       *http.Client
	metadata     MetadataReader
	cacheMu      sync.Mutex
	cache        map[string]orbitCacheEntry
	metadataGate chan struct{}
}

// MetadataReader retrieves bounded authenticated OPeNDAP responses.
type MetadataReader interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}

func NewWithMetadata(client *http.Client, reader MetadataReader) *Provider {
	p := New(client)
	p.metadata = reader
	p.cache = make(map[string]orbitCacheEntry)
	p.metadataGate = make(chan struct{}, 4)
	return p
}

func New(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &Provider{client: client}
}

func (p *Provider) Source() model.Source {
	return model.Source{
		ID:     "cloudsat",
		Name:   "CloudSat",
		Tier:   model.TierSpecial,
		Method: "CloudSat DPC THREDDS catalog + authenticated HTTPServer transfer; OPeNDAP retained for subset access",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
		},
		Description: "THREDDS catalogs are discoverable without embedding credentials. File access uses the DPC-issued THREDDS account rather than legacy SFTP as the primary path.",
	}
}

type catalog struct {
	Datasets []dataset `xml:"dataset"`
}

type dataset struct {
	Name        string       `xml:"name,attr"`
	URLPath     string       `xml:"urlPath,attr"`
	CatalogRefs []catalogRef `xml:"catalogRef"`
	Datasets    []dataset    `xml:"dataset"`
	DataSize    dataSize     `xml:"dataSize"`
	Dates       []dateValue  `xml:"date"`
}

type catalogRef struct {
	Name string `xml:"name,attr"`
	Href string `xml:"http://www.w3.org/1999/xlink href,attr"`
}

type dataSize struct {
	Units string `xml:"units,attr"`
	Value string `xml:",chardata"`
}

type dateValue struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	cat, status, err := p.fetchCatalog(ctx, catalogRoot+"/catalog.xml")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("CloudSat THREDDS root catalog returned HTTP %d", status)
	}
	refs := flattenRefs(cat.Datasets)
	out := make([]model.Collection, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		name := strings.TrimSpace(ref.Name)
		if name == "" || name == "community-products" || name == "ground_tracks" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, model.Collection{
			ID:         "cloudsat:" + name,
			SourceID:   "cloudsat",
			Satellite:  "CloudSat",
			Instrument: cloudsatInstrument(name),
			Level:      cloudsatLevel(name),
			Product:    name,
			Cadence:    "event",
			Metadata: map[string]string{
				"thredds_product_version": name,
				"version":                 cloudsatVersion(name),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	return out, nil
}

func (p *Provider) Search(ctx context.Context, q model.Query) ([]model.Granule, error) {
	if err := provider.ValidateQuery(q); err != nil {
		return nil, err
	}
	product, ok := strings.CutPrefix(q.CollectionID, "cloudsat:")
	if !ok || product == "" || strings.ContainsAny(product, "/\\") {
		return nil, provider.ErrUnsupportedCollection
	}
	if product == "3F-RMCP.R05_V0002" || product == "3S-RMCP.R05_V0002" {
		return p.searchRMCP(ctx, q, product)
	}
	withCoverage := p.metadata != nil && (product == "2B-GEOPROF.P1_R05" || product == "2B-CLDCLASS.P1_R05")
	start := q.Start.UTC()
	if withCoverage {
		start = start.Add(-6001 * time.Second)
	}
	startDay := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	out := make([]model.Granule, 0, 128)
	seen := make(map[string]bool)
	for day := startDay; day.Before(q.End); day = day.AddDate(0, 0, 1) {
		catalogURL := fmt.Sprintf("%s/%s/%04d/%03d/catalog.xml", catalogRoot, url.PathEscape(product), day.Year(), day.YearDay())
		cat, status, err := p.fetchCatalog(ctx, catalogURL)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("CloudSat THREDDS day catalog returned HTTP %d", status)
		}
		if withCoverage && len(flattenRefs(cat.Datasets)) > 0 {
			return nil, fmt.Errorf("CloudSat orbit day catalog contains unsupported catalog references")
		}
		for _, item := range flattenDatasets(cat.Datasets) {
			if item.URLPath == "" || item.Name == "" || seen[item.URLPath] {
				continue
			}
			start, ok := parseFilenameTime(item.Name)
			if !ok && withCoverage {
				return nil, fmt.Errorf("invalid CloudSat orbit filename: %s", item.Name)
			}
			if !ok || !start.Before(q.End) || (!withCoverage && start.Before(q.Start)) || (withCoverage && start.Before(q.Start.Add(-6001*time.Second))) {
				continue
			}
			var end time.Time
			var last time.Time
			if withCoverage {
				start, end, last, err = p.orbitCoverage(ctx, product, item, start)
				if err != nil {
					return nil, err
				}
				if !start.Before(q.End) || !end.After(q.Start) {
					continue
				}
			}
			seen[item.URLPath] = true
			metadata := map[string]string{
				"thredds_url_path": item.URLPath,
			}
			if item.DataSize.Value != "" {
				metadata["catalog_size"] = strings.TrimSpace(item.DataSize.Value) + " " + item.DataSize.Units
			}
			if modified := modifiedDate(item.Dates); modified != "" {
				metadata["modified"] = modified
			}
			if withCoverage {
				metadata["last_profile_time"] = last.Format(time.RFC3339Nano)
				metadata["coverage_basis"] = "profile_centres"
			}
			out = append(out, model.Granule{
				ID:           "cloudsat:" + item.Name,
				SourceID:     "cloudsat",
				CollectionID: q.CollectionID,
				Start:        start,
				End:          end,
				Assets: []model.Asset{
					{
						ID:        item.URLPath,
						Name:      item.Name,
						URL:       fileRoot + escapePath(item.URLPath),
						AuthRef:   "cloudsat",
						MediaType: "application/x-hdf",
					},
				},
				Metadata: metadata,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start.Equal(out[j].Start) {
			return out[i].ID < out[j].ID
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out, nil
}

func (p *Provider) fetchCatalog(ctx context.Context, endpoint string) (catalog, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog{}, 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return catalog{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return catalog{}, resp.StatusCode, nil
	}
	if resp.StatusCode/100 != 2 {
		return catalog{}, resp.StatusCode, nil
	}
	var payload catalog
	if err := xml.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return catalog{}, resp.StatusCode, fmt.Errorf("decode THREDDS catalog: %w", err)
	}
	return payload, resp.StatusCode, nil
}

func flattenRefs(datasets []dataset) []catalogRef {
	var out []catalogRef
	for _, item := range datasets {
		out = append(out, item.CatalogRefs...)
		out = append(out, flattenRefs(item.Datasets)...)
	}
	return out
}

func flattenDatasets(datasets []dataset) []dataset {
	var out []dataset
	for _, item := range datasets {
		if item.URLPath != "" {
			out = append(out, item)
		}
		out = append(out, flattenDatasets(item.Datasets)...)
	}
	return out
}

func parseFilenameTime(name string) (time.Time, bool) {
	base := path.Base(name)
	if len(base) < 13 {
		return time.Time{}, false
	}
	for _, char := range base[:13] {
		if char < '0' || char > '9' {
			return time.Time{}, false
		}
	}
	parsed, err := time.ParseInLocation("2006002150405", base[:13], time.UTC)
	return parsed, err == nil
}

func modifiedDate(dates []dateValue) string {
	for _, item := range dates {
		if item.Type == "modified" {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func escapePath(value string) string {
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func cloudsatInstrument(product string) string {
	upper := strings.ToUpper(product)
	if strings.Contains(upper, "AUX") {
		return "auxiliary"
	}
	if strings.HasPrefix(upper, "3") {
		return "derived"
	}
	return "CPR"
}

func cloudsatLevel(product string) string {
	upper := strings.ToUpper(product)
	for _, prefix := range []string{"1B", "2B", "2C", "2D", "3F", "3S"} {
		if strings.HasPrefix(upper, prefix) {
			return prefix
		}
	}
	if strings.Contains(upper, "AUX") {
		return "AUX"
	}
	if dot := strings.Index(product, "."); dot > 0 {
		return product[:dot]
	}
	return ""
}

func cloudsatVersion(product string) string {
	_, version, ok := strings.Cut(product, ".")
	if !ok {
		return ""
	}
	return version
}

func catalogSizeBytes(size dataSize) int64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(size.Value), 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(size.Units) {
	case "bytes":
		return int64(value)
	case "kbytes":
		return int64(value * 1000)
	case "mbytes":
		return int64(value * 1000 * 1000)
	default:
		return 0
	}
}
