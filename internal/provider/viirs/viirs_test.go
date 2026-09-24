package viirs

import "testing"

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
