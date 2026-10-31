package himawari

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
)

type Provider struct {
	satellite        int
	sourceID         string
	bucket           string
	objects          *objectstore.Client
	observationCache observationCache
	openObservation  func(context.Context, model.Asset) (io.ReadCloser, error)
}

var regionalName = regexp.MustCompile(`^HS_H0[89]_([0-9]{8})_([0-9]{4})_B[0-9]{2}_(JP0[1-4]|R30[1-4])_R[0-9]{2}_S[0-9]{4}\.DAT(?:\.bz2)?$`)

func New(satellite int, objects *objectstore.Client) (*Provider, error) {
	if satellite != 8 && satellite != 9 {
		return nil, fmt.Errorf("unsupported Himawari satellite %d", satellite)
	}
	if objects == nil {
		objects = objectstore.NewClient(nil)
	}
	p := &Provider{
		satellite: satellite,
		sourceID:  fmt.Sprintf("himawari%d", satellite),
		bucket:    fmt.Sprintf("noaa-himawari%d", satellite),
		objects:   objects,
	}
	p.openObservation = func(ctx context.Context, asset model.Asset) (io.ReadCloser, error) {
		return objects.OpenS3Prefix(ctx, p.bucket, asset.ID, maxObjectBytes, asset.ETag)
	}
	return p, nil
}

func (p *Provider) Source() model.Source {
	return model.Source{
		ID:     p.sourceID,
		Name:   fmt.Sprintf("Himawari-%d", p.satellite),
		Tier:   model.TierAnonymous,
		Method: "NOAA NODD anonymous S3 object storage",
		Capabilities: []model.Capability{
			model.CapabilityCatalog,
			model.CapabilitySearch,
			model.CapabilityDownload,
		},
		Description: "JMA AHI products distributed through NOAA's public NODD S3 buckets.",
	}
}

func (p *Provider) Collections(ctx context.Context) ([]model.Collection, error) {
	var prefixes []string
	token := ""
	for {
		page, err := p.objects.ListS3(ctx, p.bucket, "", "/", token)
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
			Satellite:  fmt.Sprintf("Himawari-%d", p.satellite),
			Instrument: instrument,
			Level:      level,
			Product:    product,
			Cadence:    himawariCadence(product),
			Metadata: map[string]string{
				"bucket":    p.bucket,
				"store":     "s3",
				"area_type": himawariArea(product),
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
		start       time.Time
		assets      []model.Asset
		observation string
	}
	groups := map[string]*grouped{}
	seen := map[string]bool{}
	regional := product == "AHI-L1b-Japan" || product == "AHI-L1b-Target"
	searchStart := q.Start.UTC()
	if regional {
		searchStart = searchStart.Truncate(10 * time.Minute).Add(-10 * time.Minute)
	}
	startDay := time.Date(searchStart.Year(), searchStart.Month(), searchStart.Day(), 0, 0, 0, 0, time.UTC)
	for day := startDay; day.Before(q.End); day = day.AddDate(0, 0, 1) {
		prefix := fmt.Sprintf("%s/%04d/%02d/%02d/", product, day.Year(), day.Month(), day.Day())
		// Himawari full-disk days contain tens of thousands of band/segment
		// objects. ListObjectsV2 is lexicographically ordered by key, so start
		// at the requested clock time instead of enumerating the whole day.
		// Once the first object at/after q.End is seen, no later page can match.
		startAfter := prefix
		if sameUTCDay(day, searchStart) {
			startAfter = prefix + searchStart.Format("1504") + "/"
		}
		token := ""
		finishedDay := false
		for {
			page, err := p.objects.ListS3After(ctx, p.bucket, prefix, "", token, startAfter)
			if err != nil {
				return nil, err
			}
			for _, object := range page.Objects {
				cycle, ok := parseCycleFromObject(product, object.Name)
				if !ok {
					continue
				}
				if !cycle.Before(q.End) {
					finishedDay = true
					break
				}
				if cycle.Before(searchStart) {
					continue
				}
				key := cycle.Format("20060102T1504Z")
				observation := ""
				if regional {
					observation, ok = regionalObservation(product, object.Name, cycle)
					if !ok {
						continue
					}
					if seen[object.Name] {
						continue
					}
					seen[object.Name] = true
					if len(seen) > maxObservationObjects {
						return nil, fmt.Errorf("HSD query object budget exceeded: > %d", maxObservationObjects)
					}
					key += ":" + observation
				}
				g := groups[key]
				if g == nil {
					g = &grouped{start: cycle, observation: observation}
					groups[key] = g
				}
				g.assets = append(g.assets, model.Asset{
					ID:        object.Name,
					Name:      filepath.Base(object.Name),
					URL:       objectstore.S3ObjectURL(p.bucket, object.Name),
					Size:      object.Size,
					ETag:      object.ETag,
					MediaType: "application/octet-stream",
				})
			}
			if finishedDay || page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
	}

	intervals := map[string]observationInterval{}
	if regional {
		var assets []model.Asset
		for _, group := range groups {
			assets = append(assets, group.assets...)
		}
		sortAssets(assets)
		values, err := p.resolveObservations(ctx, assets)
		if err != nil {
			return nil, err
		}
		for i, asset := range assets {
			intervals[asset.ID] = values[i]
		}
	}
	out := make([]model.Granule, 0, len(groups))
	for key, group := range groups {
		sort.Slice(group.assets, func(i, j int) bool { return group.assets[i].Name < group.assets[j].Name })
		end := group.start
		if himawariCadence(product) == "10m" {
			end = end.Add(10 * time.Minute)
		}
		metadata := map[string]string{"store": "s3"}
		if product == "AHI-L2-FLDK-ISatSS" {
			end = time.Time{}
			metadata["time_precision"] = "archive_minute"
		}
		if regional {
			first := intervals[group.assets[0].ID]
			start := first.start
			end = first.end
			startMJD, endMJD := first.startMJD, first.endMJD
			for _, asset := range group.assets[1:] {
				value := intervals[asset.ID]
				if value.start.Before(start) {
					start, startMJD = value.start, value.startMJD
				}
				if value.end.After(end) {
					end, endMJD = value.end, value.endMJD
				}
			}
			if !start.Before(q.End) || !end.After(q.Start) {
				continue
			}
			group.start = start
			metadata["observation_code"] = group.observation
			metadata["time_precision"] = "hsd_header_union"
			metadata["observation_start_mjd"] = fmt.Sprintf("%.17g", startMJD)
			metadata["observation_end_mjd"] = fmt.Sprintf("%.17g", endMJD)
			metadata["timeline_start"] = strings.Split(key, ":")[0]
			metadata["timeline_minutes"] = "10"
		}
		out = append(out, model.Granule{
			ID:           p.sourceID + ":" + product + ":" + key,
			SourceID:     p.sourceID,
			CollectionID: q.CollectionID,
			Start:        group.start,
			End:          end,
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

func regionalObservation(product, name string, cycle time.Time) (string, bool) {
	m := regionalName.FindStringSubmatch(filepath.Base(name))
	if m == nil || m[1]+m[2] != cycle.UTC().Format("200601021504") {
		return "", false
	}
	if product == "AHI-L1b-Japan" && strings.HasPrefix(m[3], "JP") || product == "AHI-L1b-Target" && strings.HasPrefix(m[3], "R3") {
		return m[3], true
	}
	return "", false
}

func sameUTCDay(a, b time.Time) bool {
	a = a.UTC()
	b = b.UTC()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func parseCycleFromObject(product, name string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, product+"/")
	if !ok {
		return time.Time{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 5 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 || len(parts[3]) != 4 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006/01/02/1504", strings.Join(parts[:4], "/"), time.UTC)
	return t, err == nil
}

func himawariCadence(product string) string {
	upper := strings.ToUpper(product)
	if strings.Contains(upper, "FLDK") {
		return "10m"
	}
	return "event"
}

func himawariArea(product string) string {
	if strings.Contains(strings.ToUpper(product), "FLDK") {
		return "DISK"
	}
	return ""
}
