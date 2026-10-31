package downloadcoord

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/cloudsat"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type rmcpCatalogTransport func(*http.Request) (*http.Response, error)

func (f rmcpCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudSatRMCPProviderCoverageAcrossDiscoveryChunks(t *testing.T) {
	for _, product := range []string{"3F-RMCP", "3S-RMCP"} {
		for _, tc := range []struct {
			name       string
			start, end time.Time
			want       []string
		}{
			{"left-overlap", time.Date(2007, 2, 28, 0, 0, 0, 0, time.UTC), time.Date(2007, 3, 3, 0, 0, 0, 0, time.UTC), []string{"2006-DJF", "2007-02", "2007-03"}},
			{"exclusive-end", time.Date(2007, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2007, 3, 3, 0, 0, 0, 0, time.UTC), []string{"2007-03"}},
		} {
			t.Run(product+"/"+tc.name, func(t *testing.T) {
				version := product + ".R05_V0002"
				key := "cloudsat:" + version
				name := func(period string) string { return period + "_CS_" + product + "_10x10_R05_V0002_U001.nc" }
				entry := func(period, directory string) string {
					file := name(period)
					return fmt.Sprintf(`<dataset name="%s" urlPath="cloudsat-data/%s/%s/%s"/>`, file, version, directory, file)
				}
				base := "/thredds/catalog/cloudsat-data/" + version + "/"
				catalogs := map[string]string{
					base + "catalog.xml":          `<catalog xmlns:xlink="http://www.w3.org/1999/xlink"><dataset><catalogRef xlink:href="annual/catalog.xml"/><catalogRef xlink:href="monthly/catalog.xml"/><catalogRef xlink:href="seasonal/catalog.xml"/></dataset></catalog>`,
					base + "annual/catalog.xml":   `<catalog><dataset>` + entry("2006", "annual") + `</dataset></catalog>`,
					base + "monthly/catalog.xml":  `<catalog><dataset>` + entry("2007-01", "monthly") + entry("2007-02", "monthly") + entry("2007-03", "monthly") + `</dataset></catalog>`,
					base + "seasonal/catalog.xml": `<catalog><dataset>` + entry("2006-DJF", "seasonal") + `</dataset></catalog>`,
				}
				requests := map[string]int{}
				transport := rmcpCatalogTransport(func(r *http.Request) (*http.Response, error) {
					if err := r.Context().Err(); err != nil {
						return nil, err
					}
					body, ok := catalogs[r.URL.Path]
					if r.URL.Host != "dpc-thredds.cira.colostate.edu" || !ok {
						return nil, fmt.Errorf("unexpected catalog: %s", r.URL)
					}
					requests[r.URL.Path]++
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if err := s.UpsertProducts(context.Background(), "cloudsat", []model.Product{{ProductKey: key, SourceID: "cloudsat", AreaType: "ORBIT"}}); err != nil {
					t.Fatal(err)
				}
				request, err := s.EnqueueDownloadRequest(context.Background(), "cloudsat", "cloudsat", key, tc.start, tc.end)
				if err != nil {
					t.Fatal(err)
				}
				client, mu, specs := fakeTransferClient(t)
				c, err := New(provider.NewRegistry(cloudsat.New(&http.Client{Transport: transport})), s, client)
				if err != nil {
					t.Fatal(err)
				}
				chunks := 0
				for request.Cursor.Before(request.End) {
					if err := c.advanceDiscovery(context.Background(), request); err != nil {
						t.Fatal(err)
					}
					chunks++
					request, err = s.DownloadRequest(context.Background(), request.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if chunks < 2 || requests[base+"catalog.xml"] != chunks {
					t.Fatalf("chunks=%d root requests=%d", chunks, requests[base+"catalog.xml"])
				}
				mu.Lock()
				var got []string
				for _, spec := range *specs {
					got = append(got, spec.GranuleID)
				}
				mu.Unlock()
				var want []string
				for _, period := range tc.want {
					want = append(want, "cloudsat:"+name(period))
				}
				sort.Strings(got)
				sort.Strings(want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("granules got %v, want %v", got, want)
				}
			})
		}
	}
}
