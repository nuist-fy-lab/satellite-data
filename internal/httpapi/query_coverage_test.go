package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/cloudsat"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/himawari"
)

type coverageTransport func(*http.Request) (*http.Response, error)

func (f coverageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type boundaryQueryProvider struct{ items []model.Granule }

func (*boundaryQueryProvider) Source() model.Source { return model.Source{ID: "test"} }
func (*boundaryQueryProvider) Collections(context.Context) ([]model.Collection, error) {
	return nil, nil
}
func (p *boundaryQueryProvider) Search(context.Context, model.Query) ([]model.Granule, error) {
	return p.items, nil
}

func requestCoverage(t *testing.T, p provider.Provider, collection string, start, end time.Time) []model.Granule {
	t.Helper()
	values := url.Values{"collection": {collection}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}}
	w := httptest.NewRecorder()
	New(provider.NewRegistry(p), nil).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/search?"+values.Encode(), nil))
	var body struct {
		Granules []model.Granule `json:"granules"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("search: %d %s", w.Code, w.Body.String())
	}
	return body.Granules
}

func TestSearchCoverageHalfOpenAndUnknownEnd(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	p := &boundaryQueryProvider{items: []model.Granule{
		{ID: "left", Start: start.Add(-time.Minute), End: start.Add(time.Second)},
		{ID: "ends-at-start", Start: start.Add(-time.Minute), End: start},
		{ID: "starts-at-end", Start: end, End: end.Add(time.Minute)},
		{ID: "point-inside", Start: start}, {ID: "point-before", Start: start.Add(-time.Second)},
		{ID: "invalid-before", Start: start.Add(-time.Second), End: start.Add(-2 * time.Second)},
		{ID: "invalid-inside", Start: start.Add(time.Second), End: start},
	}}
	got := requestCoverage(t, p, "test:collection", start, end)
	ids := map[string]bool{}
	for _, g := range got {
		ids[g.ID] = true
	}
	if len(ids) != 3 || !ids["left"] || !ids["point-inside"] || !ids["invalid-inside"] {
		t.Fatalf("unexpected coverage: %+v", got)
	}
}

func TestRegionalInteractiveLimitBeforeProviderAccess(t *testing.T) {
	for _, satellite := range []int{8, 9} {
		calls := 0
		client := &http.Client{Transport: coverageTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<ListBucketResult/>"))}, nil
		})}
		p, err := himawari.New(satellite, objectstore.NewClient(client))
		if err != nil {
			t.Fatal(err)
		}
		for _, product := range []string{"AHI-L1b-Japan", "AHI-L1b-Target"} {
			values := url.Values{"collection": {fmt.Sprintf("himawari%d:%s", satellite, product)}, "start": {"2024-01-01T00:00:00Z"}, "end": {"2024-01-01T00:10:01Z"}}
			w := httptest.NewRecorder()
			New(provider.NewRegistry(p), nil).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/search?"+values.Encode(), nil))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "区域观测查询请缩小到10分钟") || calls != 0 {
				t.Fatalf("status %d calls %d: %s", w.Code, calls, w.Body.String())
			}
		}
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		requestCoverage(t, p, fmt.Sprintf("himawari%d:AHI-L1b-Japan", satellite), start, start.Add(10*time.Minute))
		if calls == 0 {
			t.Fatal("ten-minute query did not reach provider")
		}
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &boundaryQueryProvider{items: []model.Granule{{ID: "point", Start: start}}}
	if got := requestCoverage(t, p, "test:collection", start, start.Add(time.Hour)); len(got) != 1 {
		t.Fatalf("unrelated product limited: %+v", got)
	}
}

func TestSearchHimawariHeaderOverlap(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("QlpoOTFBWSZTWZkBFX4AAA5/j3G4AnAAA3wgfVJcEKAyEaBAEEAAAAgAAiAAcjPSAaYgMjQAABiEEao9JoNpPaoZGRo8p7UQ0IcoYGP0qCHYQGQDAhr99up1c2K+Xo243BtFWLhNsWeHnTqFSAKSfGiJ/hm6p2eJqkmslkDd5XJkkEdPXW0Ez+KL7gpgctlBa8mED8XckU4UJCZARV+A")
	if err != nil {
		t.Fatal(err)
	}
	key := "AHI-L1b-Japan/2024/01/01/0000/HS_H09_20240101_0000_B01_JP01_R10_S0101.DAT.bz2"
	client := &http.Client{Transport: coverageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("list-type") != "" {
			body := "<ListBucketResult>"
			if r.URL.Query().Get("prefix") == "AHI-L1b-Japan/2024/01/01/" {
				body += "<Contents><Key>" + key + "</Key></Contents>"
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body + "</ListBucketResult>"))}, nil
		}
		header := make(http.Header)
		header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		return &http.Response{StatusCode: 206, Header: header, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	p, err := himawari.New(9, objectstore.NewClient(client))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 1, 0, 0, time.UTC)
	got := requestCoverage(t, p, "himawari9:AHI-L1b-Japan", start, start.Add(time.Second))
	if len(got) != 1 || !got[0].Start.Before(start) || !got[0].End.After(start) {
		t.Fatalf("header overlap: %+v", got)
	}
}

func TestSearchCloudSatSeasonalCrossYearOverlap(t *testing.T) {
	for _, code := range []string{"3F-RMCP.R05_V0002", "3S-RMCP.R05_V0002"} {
		t.Run(code, func(t *testing.T) {
			name := "2008-DJF_CS_" + strings.Split(code, ".")[0] + "_10x10_R05_V0002_U001.nc"
			client := &http.Client{Transport: coverageTransport(func(r *http.Request) (*http.Response, error) {
				body := `<catalog xmlns:xlink="http://www.w3.org/1999/xlink"><dataset><catalogRef xlink:href="seasonal/catalog.xml"/></dataset></catalog>`
				if strings.HasSuffix(r.URL.Path, "/seasonal/catalog.xml") {
					body = `<catalog><dataset><dataset name="` + name + `" urlPath="cloudsat-data/` + code + `/seasonal/` + name + `"/></dataset></catalog>`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			start := time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC)
			got := requestCoverage(t, cloudsat.New(client), "cloudsat:"+code, start, start.AddDate(0, 0, 1))
			if len(got) != 1 || !got[0].Start.Before(start) || !got[0].End.After(start) {
				t.Fatalf("season overlap: %+v", got)
			}
		})
	}
}
