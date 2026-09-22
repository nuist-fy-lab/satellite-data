package calipso

import "testing"

func TestPreferredHTTPSLink(t *testing.T) {
	links := []cmrLink{
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/s3#", Href: "s3://asdc-prod-protected/example.hdf"},
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Href: "https://data.asdc.earthdata.nasa.gov/asdc-prod-protected/example.hdf"},
		{Rel: "http://esipfed.org/ns/fedsearch/1.1/data#", Href: "https://example.invalid/inherited", Inherited: true},
	}
	want := "https://data.asdc.earthdata.nasa.gov/asdc-prod-protected/example.hdf"
	if got := preferredHTTPSLink(links); got != want {
		t.Fatalf("preferredHTTPSLink() = %q, want %q", got, want)
	}
}
