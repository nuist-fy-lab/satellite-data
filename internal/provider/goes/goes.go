package goes

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

type Provider struct {
	satellite int
	sourceID  string
	bucket    string
	objects   *objectstore.Client
}

var archiveName = regexp.MustCompile(`^OR_([A-Za-z0-9-]+)_G(1[6-9])_s([0-9]{13,22})_e[0-9]{13,22}_c[0-9]{13,22}\.nc$`)
var archiveScanMode = regexp.MustCompile(`-M[0-9](C[0-9]{2})?$`)
var filenameScanMode = regexp.MustCompile(`-M([0-9])(?:C[0-9]{2})?_G1[6-9]_`)

// ArchiveIdentity uses NOAA's timestamp, not the local directory date.
func ArchiveIdentity(name string) (string, time.Time, bool) {
	match := archiveName.FindStringSubmatch(name)
	if match == nil {
		return "", time.Time{}, false
	}
	start, ok := parseGOESCode(match[3])
	if !ok || start.Format("2006002150405") != match[3][:13] {
		return "", time.Time{}, false
	}
	product := archiveScanMode.ReplaceAllString(match[1], "")
	if strings.HasPrefix(product, "ABI-") && (strings.HasSuffix(product, "M1") || strings.HasSuffix(product, "M2")) {
		product = product[:len(product)-1]
	}
	return "goes" + match[2] + ":" + product, start, true
}

func New(satellite int, objects *objectstore.Client) (*Provider, error) {
	if satellite < 16 || satellite > 19 {
		return nil, fmt.Errorf("unsupported GOES satellite %d", satellite)
	}
	if objects == nil {
		objects = objectstore.NewClient(nil)
	}
	return &Provider{
		satellite: satellite,
		sourceID:  fmt.Sprintf("goes%d", satellite),
		bucket:    fmt.Sprintf("gcp-public-data-goes-%d", satellite),
		objects:   objects,
	}, nil
}

func (p *Provider) Source() model.Source {
	return model.Source{
		ID:     p.sourceID,
		Name:   fmt.Sprintf("GOES-%d", p.satellite),
		Tier:   model.TierAnonymous,
		Method: "NOAA NODD public object storage; GCS discovery/download is primary for this deployment",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
		},
		Description: "Native NOAA ABI/GLM/etc. products from the public GOES object hierarchy.",
	}
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	var prefixes []string
	token := ""
	for {
		page, err := p.objects.ListGCS(ctx, p.bucket, "", "/", token)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, page.Prefixes...)
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	out := make([]model.Collection, 0, len(prefixes))
	for _, prefix := range prefixes {
		product := strings.TrimSuffix(prefix, "/")
		if product == "" || strings.Contains(product, "/") {
			continue
		}
		instrument, level := provider.ProductMetadata(product)
		out = append(out, model.Collection{
			ID:         p.sourceID + ":" + product,
			SourceID:   p.sourceID,
			Satellite:  fmt.Sprintf("GOES-%d", p.satellite),
			Instrument: instrument,
			Level:      level,
			Product:    product,
			Cadence:    goesCadence(product),
			Metadata: map[string]string{
				"bucket":    p.bucket,
				"store":     "gcs",
				"area_type": goesArea(product),
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
	product, ok := strings.CutPrefix(q.CollectionID, p.sourceID+":")
	if !ok || product == "" || strings.Contains(product, "/") {
		return nil, provider.ErrUnsupportedCollection
	}

	type grouped struct {
		start        time.Time
		end          time.Time
		assets       []model.Asset
		mode         string
		modeConflict bool
	}
	groups := map[string]*grouped{}
	seen := map[string]bool{}
	flood := floodProduct(product)
	consume := func(page objectstore.Page) {
		for _, object := range page.Objects {
			start, end, key, ok := parseGOESObjectTime(filepath.Base(object.Name))
			if flood {
				start, end, key, ok = parseFloodObjectTime(filepath.Base(object.Name))
				key = object.Name
			}
			if !ok || !start.Before(q.End) || (start.Before(q.Start) && (end.IsZero() || !end.After(q.Start))) || seen[object.Name] {
				continue
			}
			seen[object.Name] = true
			mode := ""
			if match := filenameScanMode.FindStringSubmatch(filepath.Base(object.Name)); match != nil {
				mode = match[1]
			}
			g := groups[key]
			if g == nil {
				g = &grouped{start: start, end: end, mode: mode}
				groups[key] = g
			} else if g.mode != mode {
				g.modeConflict = true
			}
			if end.After(g.end) {
				g.end = end
			}
			mediaType := "application/x-netcdf"
			if flood && strings.HasSuffix(object.Name, ".tif") {
				mediaType = "image/tiff"
			} else if flood && strings.HasSuffix(object.Name, ".zip") {
				mediaType = "application/zip"
			}
			g.assets = append(g.assets, model.Asset{
				ID: object.Name, Name: filepath.Base(object.Name), URL: objectstore.GCSObjectURL(p.bucket, object.Name),
				Size: object.Size, ETag: object.ETag, MediaType: mediaType,
			})
		}
	}
	lookback := 24 * time.Hour
	if strings.HasPrefix(product, "ABI-L1b-") || strings.HasPrefix(product, "GLM-") {
		lookback = time.Hour
	}
	first := q.Start.Add(-lookback)
	if flood {
		firstDay := time.Date(first.UTC().Year(), first.UTC().Month(), first.UTC().Day(), 0, 0, 0, 0, time.UTC)
		for day := firstDay; day.Before(q.End); day = day.AddDate(0, 0, 1) {
			prefix := fmt.Sprintf("%s/%04d/%02d/%02d/", product, day.Year(), day.Month(), day.Day())
			token := ""
			for {
				page, err := p.objects.ListGCS(ctx, p.bucket, prefix, "", token)
				if err != nil {
					return nil, err
				}
				consume(page)
				if page.NextToken == "" {
					break
				}
				token = page.NextToken
			}
		}
	} else if q.End.Sub(q.Start) <= 3*time.Hour {
		for hour := first.UTC().Truncate(time.Hour); hour.Before(q.End); hour = hour.Add(time.Hour) {
			prefix := fmt.Sprintf("%s/%04d/%03d/%02d/", product, hour.Year(), hour.YearDay(), hour.Hour())
			token := ""
			for {
				page, err := p.objects.ListGCS(ctx, p.bucket, prefix, "", token)
				if err != nil {
					return nil, err
				}
				consume(page)
				if page.NextToken == "" {
					break
				}
				token = page.NextToken
			}
		}
	} else {
		firstDay := time.Date(first.UTC().Year(), first.UTC().Month(), first.UTC().Day(), 0, 0, 0, 0, time.UTC)
		for day := firstDay; day.Before(q.End); day = day.AddDate(0, 0, 1) {
			prefix := fmt.Sprintf("%s/%04d/%03d/", product, day.Year(), day.YearDay())
			token := ""
			for {
				page, err := p.objects.ListGCS(ctx, p.bucket, prefix, "", token)
				if err != nil {
					return nil, err
				}
				consume(page)
				if page.NextToken == "" {
					break
				}
				token = page.NextToken
			}
		}
	}

	out := make([]model.Granule, 0, len(groups))
	for key, group := range groups {
		sort.Slice(group.assets, func(i, j int) bool { return group.assets[i].Name < group.assets[j].Name })
		metadata := map[string]string{"satellite": strconv.Itoa(p.satellite), "store": "gcs"}
		if group.mode != "" && !group.modeConflict {
			metadata["scan_mode"] = group.mode
		}
		out = append(out, model.Granule{
			ID:           p.sourceID + ":" + product + ":" + key,
			SourceID:     p.sourceID,
			CollectionID: q.CollectionID,
			Start:        group.start,
			End:          group.end,
			Assets:       group.assets,
			Metadata:     metadata,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start.Equal(out[j].Start) {
			return out[i].ID < out[j].ID
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out, nil
}

func floodProduct(product string) bool {
	switch product {
	case "ABI-Flood-Day", "ABI-Flood-Day-TIF", "ABI-Flood-Day-Shapefiles", "ABI-Flood-Hourly", "ABI-Flood-Hourly-TIF", "ABI-Flood-Hourly-Shapefiles":
		return true
	default:
		return false
	}
}

func parseFloodObjectTime(name string) (time.Time, time.Time, string, bool) {
	startCode, ok := markerCode(name, "_s")
	if !ok || len(startCode) != 15 {
		return time.Time{}, time.Time{}, "", false
	}
	parse := func(code string) time.Time {
		if len(code) != 15 {
			return time.Time{}
		}
		t, err := time.Parse("20060102150405.0", code[:14]+"."+code[14:])
		if err != nil {
			return time.Time{}
		}
		return t
	}
	start := parse(startCode)
	if start.IsZero() {
		return time.Time{}, time.Time{}, "", false
	}
	endCode, _ := markerCode(name, "_e")
	return start, parse(endCode), name, true
}

func goesArea(product string) string {
	upper := strings.ToUpper(product)
	if !strings.HasPrefix(upper, "ABI-") {
		return ""
	}
	switch {
	case strings.HasSuffix(upper, "F") || strings.Contains(upper, "RADF"):
		return "DISK"
	case strings.HasSuffix(upper, "C") || strings.Contains(upper, "RADC"):
		return "CONUS"
	case strings.HasSuffix(upper, "M") || strings.Contains(upper, "RADM"):
		return "MESO"
	default:
		return ""
	}
}

func parseGOESObjectTime(name string) (time.Time, time.Time, string, bool) {
	startCode, ok := markerCode(name, "_s")
	if !ok {
		return time.Time{}, time.Time{}, "", false
	}
	endCode, _ := markerCode(name, "_e")
	start, ok := parseGOESCode(startCode)
	if !ok {
		return time.Time{}, time.Time{}, "", false
	}
	end, _ := parseGOESCode(endCode)
	return start, end, startCode, true
}

func markerCode(name, marker string) (string, bool) {
	idx := strings.Index(name, marker)
	if idx < 0 {
		return "", false
	}
	start := idx + len(marker)
	end := start
	for end < len(name) && name[end] >= '0' && name[end] <= '9' {
		end++
	}
	if end-start < 13 {
		return "", false
	}
	return name[start:end], true
}

func parseGOESCode(code string) (time.Time, bool) {
	if len(code) < 13 {
		return time.Time{}, false
	}
	base, err := time.ParseInLocation("2006002150405", code[:13], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	if len(code) > 13 {
		fraction := code[13:]
		if len(fraction) > 9 {
			fraction = fraction[:9]
		}
		n, err := strconv.Atoi(fraction)
		if err == nil {
			for i := len(fraction); i < 9; i++ {
				n *= 10
			}
			base = base.Add(time.Duration(n))
		}
	}
	return base, true
}

func goesCadence(product string) string {
	upper := strings.ToUpper(product)
	if !strings.HasPrefix(upper, "ABI-") {
		return "event"
	}
	switch {
	case strings.HasSuffix(upper, "F") || strings.Contains(upper, "RADF"):
		return "10m"
	case strings.HasSuffix(upper, "C") || strings.Contains(upper, "RADC"):
		return "5m"
	case strings.HasSuffix(upper, "M") || strings.Contains(upper, "RADM"):
		return "1m"
	default:
		return "event"
	}
}
