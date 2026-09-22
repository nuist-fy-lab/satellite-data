package laads

import (
	"strings"
	"testing"
)

func TestMODISSatellite(t *testing.T) {
	if got := modisSatellite([]string{"Terra"}); got != "Terra" {
		t.Fatalf("got=%q", got)
	}
	if got := modisSatellite([]string{"Aqua"}); got != "Aqua" {
		t.Fatalf("got=%q", got)
	}
	if got := modisSatellite([]string{"Terra", "Aqua"}); got != "Terra / Aqua" {
		t.Fatalf("got=%q", got)
	}
}

func TestMODISResolution(t *testing.T) {
	got := modisResolution("MODIS Terra product 250m, 500m, 1km")
	if got != "250m/500m/1km" {
		t.Fatalf("got=%q", got)
	}
}

func TestMODISCadence(t *testing.T) {
	if got := modisCadence("Calibrated Radiances 5-Min L1B Swath 1km"); got != "5m" {
		t.Fatalf("got=%q", got)
	}
}

func TestMODISDataLinkPrefersEarthdataCloud(t *testing.T) {
	links := []cmrLink{
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Href: "https://ladsweb.modaps.eosdis.nasa.gov/archive/example.hdf"},
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Href: "https://data.laadsdaac.earthdatacloud.nasa.gov/prod-lads/example.hdf"},
	}
	got, ok := modisDataLink(links)
	if !ok || !strings.HasPrefix(got.Href, "https://data.laadsdaac.earthdatacloud.nasa.gov/") {
		t.Fatalf("unexpected link: %#v", got)
	}
}
