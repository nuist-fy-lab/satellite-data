package calipso

import "testing"

func TestCALIPSOProcessingLevels(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"1", "L1"}, {"1B", "L1B"}, {" 2 ", "L2"}, {"L2", "L2"}, {"l1b", "L1B"}, {"", ""},
	} {
		if got := calipsoLevel(tc.input); got != tc.want {
			t.Errorf("calipsoLevel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

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
