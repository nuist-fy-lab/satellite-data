package cloudsat

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// RMCP filenames encode calendar periods, unlike orbit granule filenames.
func rmcpCoverage(name, product string) (time.Time, time.Time, bool) {
	parts := strings.SplitN(path.Base(name), "_", 4)
	if len(parts) < 4 || parts[1] != "CS" || parts[2] != strings.SplitN(product, ".", 2)[0] || !strings.HasSuffix(name, ".nc") {
		return time.Time{}, time.Time{}, false
	}
	p := strings.Split(parts[0], "-")
	if len(p) != 1 && len(p) != 2 && len(p) != 4 {
		return time.Time{}, time.Time{}, false
	}
	year, err := strconv.Atoi(p[0])
	if err != nil || len(p[0]) != 4 || year < 1 {
		return time.Time{}, time.Time{}, false
	}
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	if len(p) == 1 {
		return start, start.AddDate(1, 0, 0), true
	}
	seasons := map[string]time.Month{"DJF": time.December, "MAM": time.March, "JJA": time.June, "SON": time.September}
	if month, ok := seasons[p[1]]; ok && len(p) == 2 {
		start = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 3, 0), true
	}
	month, err := strconv.Atoi(p[1])
	if err != nil || len(p[1]) != 2 || month < 1 || month > 12 {
		return time.Time{}, time.Time{}, false
	}
	start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	if len(p) == 2 {
		return start, start.AddDate(0, 1, 0), true
	}
	endYear, e1 := strconv.Atoi(p[2])
	endMonth, e2 := strconv.Atoi(p[3])
	if e1 != nil || e2 != nil || len(p[2]) != 4 || len(p[3]) != 2 || endYear < 1 || endMonth < 1 || endMonth > 12 {
		return time.Time{}, time.Time{}, false
	}
	end := time.Date(endYear, time.Month(endMonth), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return start, end, end.After(start)
}

func (p *Provider) searchRMCP(ctx context.Context, q model.Query, product string) ([]model.Granule, error) {
	root := catalogRoot + "/" + url.PathEscape(product) + "/"
	base, _ := url.Parse(root)
	queue := []string{root + "catalog.xml"}
	visited := map[string]bool{}
	seen := map[string]bool{}
	var out []model.Granule
	for len(queue) > 0 {
		endpoint := queue[0]
		queue = queue[1:]
		if visited[endpoint] {
			continue
		}
		visited[endpoint] = true
		cat, status, err := p.fetchCatalog(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("CloudSat RMCP catalog returned HTTP %d: %s", status, endpoint)
		}
		current, _ := url.Parse(endpoint)
		for _, ref := range flattenRefs(cat.Datasets) {
			u, err := url.Parse(ref.Href)
			if err != nil || ref.Href == "" {
				return nil, fmt.Errorf("invalid CloudSat catalog reference")
			}
			u = current.ResolveReference(u)
			if u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(u.Path, base.Path) || u.RawQuery != "" || u.Fragment != "" {
				return nil, fmt.Errorf("CloudSat catalog reference outside product: %s", ref.Href)
			}
			queue = append(queue, u.String())
		}
		for _, item := range flattenDatasets(cat.Datasets) {
			if item.Name == "" || item.URLPath == "" || seen[item.URLPath] {
				continue
			}
			start, end, ok := rmcpCoverage(item.Name, product)
			if !ok {
				return nil, fmt.Errorf("unrecognized CloudSat RMCP filename: %s", item.Name)
			}
			if !start.Before(q.End) || !end.After(q.Start) {
				continue
			}
			seen[item.URLPath] = true
			metadata := map[string]string{"thredds_url_path": item.URLPath}
			if item.DataSize.Value != "" {
				metadata["catalog_size"] = strings.TrimSpace(item.DataSize.Value) + " " + item.DataSize.Units
			}
			if modified := modifiedDate(item.Dates); modified != "" {
				metadata["modified"] = modified
			}
			out = append(out, model.Granule{ID: "cloudsat:" + item.Name, SourceID: "cloudsat", CollectionID: q.CollectionID, Start: start, End: end, Assets: []model.Asset{{ID: item.URLPath, Name: item.Name, URL: fileRoot + escapePath(item.URLPath), AuthRef: "cloudsat", MediaType: "application/x-netcdf"}}, Metadata: metadata})
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
