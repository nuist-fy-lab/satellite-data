package pipeline

import "testing"

func TestAllowedHostAcceptsNativeLAADS(t *testing.T) {
	for _, source := range []string{"modis", "viirs"} {
		if !allowedHost(source, "ladsweb.modaps.eosdis.nasa.gov") {
			t.Fatalf("%s should allow native LAADS archive", source)
		}
		if !allowedHost(source, "data.laadsdaac.earthdatacloud.nasa.gov") {
			t.Fatalf("%s should retain Earthdata Cloud compatibility", source)
		}
	}
	if allowedHost("modis", "example.invalid") {
		t.Fatal("unexpected host accepted")
	}
}
