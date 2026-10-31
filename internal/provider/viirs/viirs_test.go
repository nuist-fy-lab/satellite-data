package viirs

import (
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

func TestGranuleSearchRejectsIncompletePagination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     func(int) string
		emptyAt   int
		timedOut  bool
		wantError bool
	}{
		{"complete", func(i int) string { return "" }, 0, false, false},
		{"missing", func(i int) string { return "" }, -1, false, true},
		{"repeated", func(i int) string { return "same" }, -1, false, true},
		{"limit", func(i int) string { return fmt.Sprint(i) }, -1, false, true},
		{"timeout", func(i int) string { return "one" }, -1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("CMR-Search-After", tc.token(i))
				if tc.timedOut {
					h.Set("CMR-Timed-Out", "true")
				}
				body := `{"feed":{"entry":[` + strings.TrimSuffix(strings.Repeat(`{},`, 2000), ",") + `]}}`
				if i == tc.emptyAt {
					body = `{"feed":{"entry":[]}}`
				}
				i++
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			p := New(client)
			p.catalog = []model.Collection{{ID: "viirs:test"}}
			p.catalogTime = time.Now()
			start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			_, err := p.Search(t.Context(), model.Query{CollectionID: "viirs:test", Start: start, End: start.Add(time.Hour)})
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v pages=%d", err, i)
			}
		})
	}
}

func TestVIIRSSatellite(t *testing.T) {
	cases := map[string]string{
		"Suomi-NPP": "Suomi-NPP",
		"NOAA-20":   "NOAA-20",
		"NOAA-21":   "NOAA-21",
	}
	for input, want := range cases {
		if got := viirsSatellite([]string{input}); got != want {
			t.Fatalf("platform %q => %q want %q", input, got, want)
		}
	}
}

func TestVIIRSResolution(t *testing.T) {
	if got := viirsResolution("VIIRS Imagery 6-Min L1B Swath 375 m"); got != "375m" {
		t.Fatalf("resolution=%q", got)
	}
}

func TestVIIRSDataLinkPrefersNativeLAADSArchive(t *testing.T) {
	links := []cmrLink{
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/service#", Href: "https://ladsweb.modaps.eosdis.nasa.gov/opendap/RemoteResources/laads/allData/5200/CLDMSK_L2_VIIRS_NOAA20/2026/265/example.nc.html"},
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Href: "https://data.laadsdaac.earthdatacloud.nasa.gov/prod-lads/example.nc"},
	}
	got, ok := viirsDataLink(links)
	if !ok || !strings.HasPrefix(got.Href, "https://ladsweb.modaps.eosdis.nasa.gov/archive/allData/5200/") || strings.HasSuffix(got.Href, ".html") {
		t.Fatalf("unexpected link: %#v", got)
	}
}
