package viirs

import (
	"strings"
	"testing"
)

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
