package cloudsat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRMCPCoverage(t *testing.T) {
	cases := []struct {
		name, start, end string
		ok               bool
	}{
		{"2008-DJF", "2008-12-01", "2009-03-01", true},
		{"2008-02", "2008-02-01", "2008-03-01", true},
		{"2008", "2008-01-01", "2009-01-01", true},
		{"2008-12-2009-02", "2008-12-01", "2009-03-01", true},
		{"2008-13", "", "", false}, {"2009-02-2008-12", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e, ok := rmcpCoverage(tc.name+"_CS_3F-RMCP_10x10_R05_V0002_U001.nc", "3F-RMCP.R05_V0002")
			if ok != tc.ok || ok && (s.Format("2006-01-02") != tc.start || e.Format("2006-01-02") != tc.end) {
				t.Fatalf("got %v %v %v", s, e, ok)
			}
		})
	}
}

func TestSearchRMCP(t *testing.T) {
	file := func(name string) string {
		return fmt.Sprintf(`<dataset name="%s" urlPath="cloudsat-data/p/%s"/>`, name, name)
	}
	a := "2008-DJF_CS_3F-RMCP_10x10_R05_V0002_U001.nc"
	b := "2009-03_CS_3F-RMCP_10x10_R05_V0002_U001.nc"
	catalogs := map[string]string{
		"/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/catalog.xml":          `<catalog xmlns:xlink="http://www.w3.org/1999/xlink"><dataset><catalogRef xlink:href="seasonal/catalog.xml"/><catalogRef xlink:href="monthly/catalog.xml"/></dataset></catalog>`,
		"/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/seasonal/catalog.xml": `<catalog><dataset>` + file(a) + file(a) + `</dataset></catalog>`,
		"/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/monthly/catalog.xml":  `<catalog><dataset>` + file(b) + `</dataset></catalog>`,
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		body, ok := catalogs[r.URL.Path]
		status := 200
		if !ok {
			status = 404
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := New(client)
	q := model.Query{CollectionID: "cloudsat:3F-RMCP.R05_V0002", Start: time.Date(2009, 2, 28, 0, 0, 0, 0, time.UTC), End: time.Date(2009, 3, 1, 0, 0, 0, 0, time.UTC)}
	got, err := p.Search(context.Background(), q)
	if err != nil || len(got) != 1 || got[0].Assets[0].Name != a || !got[0].End.Equal(q.End) {
		t.Fatalf("got %#v, %v", got, err)
	}
	delete(catalogs, "/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/monthly/catalog.xml")
	if _, err := p.Search(context.Background(), q); err == nil {
		t.Fatal("missing referenced catalog must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, q); err == nil {
		t.Fatal("cancellation must fail")
	}
}

func TestSearch3SRMCPSeasonal(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/catalog.xml":
			body = `<catalog xmlns:xlink="http://www.w3.org/1999/xlink"><dataset><catalogRef xlink:href="seasonal/catalog.xml"/></dataset></catalog>`
		case "/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/seasonal/catalog.xml":
			body = `<catalog><dataset><dataset name="2006-DJF_CS_3S-RMCP_10x10_R05_V0002_U001.nc" urlPath="cloudsat-data/3S-RMCP.R05_V0002/seasonal/2006-DJF_CS_3S-RMCP_10x10_R05_V0002_U001.nc"/></dataset></catalog>`
		default:
			return nil, fmt.Errorf("unexpected catalog %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	q := model.Query{CollectionID: "cloudsat:3S-RMCP.R05_V0002", Start: time.Date(2007, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2007, 1, 2, 0, 0, 0, 0, time.UTC)}
	got, err := New(client).Search(context.Background(), q)
	if err != nil || len(got) != 1 || !got[0].Start.Equal(time.Date(2006, 12, 1, 0, 0, 0, 0, time.UTC)) || !got[0].End.Equal(time.Date(2007, 3, 1, 0, 0, 0, 0, time.UTC)) || got[0].Assets[0].MediaType != "application/x-netcdf" {
		t.Fatalf("got %#v, %v", got, err)
	}
}
