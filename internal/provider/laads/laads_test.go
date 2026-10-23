package laads

import (
	"strings"
	"testing"
	"time"
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

func TestMODISDataLinkPrefersNativeLAADSArchive(t *testing.T) {
	links := []cmrLink{
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/service#", Href: "https://ladsweb.modaps.eosdis.nasa.gov/opendap/RemoteResources/laads/allData/5110/CLDMSK_L2_MODIS_Aqua/2026/265/example.nc.html"},
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Href: "https://data.laadsdaac.earthdatacloud.nasa.gov/prod-lads/example.hdf"},
	}
	got, ok := modisDataLink(links)
	if !ok || !strings.HasPrefix(got.Href, "https://ladsweb.modaps.eosdis.nasa.gov/archive/allData/5110/") || strings.HasSuffix(got.Href, ".html") {
		t.Fatalf("unexpected link: %#v", got)
	}
	if got.Type != "application/x-netcdf" {
		t.Fatalf("OPeNDAP HTML service misclassified native NetCDF: %q", got.Type)
	}
}

func TestMODISC7NetCDFGranule(t *testing.T) {
	// CMR example: MYD021KM C7, 2020-03-09 23:15 UTC. Its archive
	// download is NetCDF even though the corresponding service is HTML.
	const name = "MYD021KM.A2020069.2315.007.2025107172609.nc"
	const service = "https://ladsweb.modaps.eosdis.nasa.gov/opendap/RemoteResources/laads/allData/7/MYD021KM/2020/069/" + name + ".html"
	const cloud = "https://data.laadsdaac.earthdatacloud.nasa.gov/prod-lads/MYD021KM/" + name
	item := cmrGranule{
		ID: "G4019566795-LAADS", CollectionConceptID: "C3861713185-LAADS", ProducerGranuleID: name,
		TimeStart: "2020-03-09T23:15:00.000Z", TimeEnd: "2020-03-09T23:20:00.000Z",
		Links: []cmrLink{
			{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Type: "application/x-netcdf", Href: cloud},
			{Rel: "http://esipfed.org/ns/fedsearch/1.1/service#", Type: "text/html", Href: service},
		},
	}
	granule, ok := cmrGranuleModel(item, "modis:C3861713185-LAADS")
	if !ok || len(granule.Assets) != 1 {
		t.Fatalf("missing C7 granule: %+v ok=%v", granule, ok)
	}
	asset := granule.Assets[0]
	if asset.Name != name || asset.MediaType != "application/x-netcdf" || asset.AuthRef != "laads" {
		t.Fatalf("incorrect C7 NetCDF asset: %+v", asset)
	}
	if asset.URL != "https://ladsweb.modaps.eosdis.nasa.gov/archive/allData/7/MYD021KM/2020/069/"+name {
		t.Fatalf("incorrect official C7 file URL: %s", asset.URL)
	}
	if !granule.Start.Equal(time.Date(2020, 3, 9, 23, 15, 0, 0, time.UTC)) {
		t.Fatalf("incorrect C7 granule time: %v", granule.Start)
	}
}

func TestMODISMediaTypeFollowsScientificFileNotService(t *testing.T) {
	for _, test := range []struct{ name, want string }{
		{"MYD021KM.A2020069.2315.007.2025107172609.nc", "application/x-netcdf"},
		{"MYD021KM.A2020069.2315.061.2020070170000.hdf", "application/x-hdf"},
	} {
		if got := modisMediaType("text/html", test.name); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}
