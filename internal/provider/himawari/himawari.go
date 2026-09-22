package himawari

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
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

func New(satellite int, objects *objectstore.Client) (*Provider, error) {
	if satellite != 8 && satellite != 9 {
		return nil, fmt.Errorf("unsupported Himawari satellite %d", satellite)
	}
	if objects == nil {
		objects = objectstore.NewClient(nil)
	}
	return &Provider{
		satellite: satellite,
		sourceID:  fmt.Sprintf("himawari%d", satellite),
		bucket:    fmt.Sprintf("noaa-himawari%d", satellite),
		objects:   objects,
	}, nil
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
			model.CapabilitySubscription,
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
		start  time.Time
		assets []model.Asset
	}
	groups := map[string]*grouped{}
	startDay := time.Date(q.Start.UTC().Year(), q.Start.UTC().Month(), q.Start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	for day := startDay; day.Before(q.End); day = day.AddDate(0, 0, 1) {
		prefix := fmt.Sprintf("%s/%04d/%02d/%02d/", product, day.Year(), day.Month(), day.Day())
		// Himawari full-disk days contain tens of thousands of band/segment
		// objects. ListObjectsV2 is lexicographically ordered by key, so start
		// at the requested clock time instead of enumerating the whole day.
		// Once the first object at/after q.End is seen, no later page can match.
		startAfter := prefix
		if sameUTCDay(day, q.Start) {
			startAfter = prefix + q.Start.UTC().Format("1504") + "/"
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
				if cycle.Before(q.Start) {
					continue
				}
				key := cycle.Format("20060102T1504Z")
				g := groups[key]
				if g == nil {
					g = &grouped{start: cycle}
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

	out := make([]model.Granule, 0, len(groups))
	for key, group := range groups {
		sort.Slice(group.assets, func(i, j int) bool { return group.assets[i].Name < group.assets[j].Name })
		end := group.start
		if himawariCadence(product) == "10m" {
			end = end.Add(10 * time.Minute)
		}
		out = append(out, model.Granule{
			ID:           p.sourceID + ":" + product + ":" + key,
			SourceID:     p.sourceID,
			CollectionID: q.CollectionID,
			Start:        group.start,
			End:          end,
			Assets:       group.assets,
			Metadata: map[string]string{
				"store": "s3",
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
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
