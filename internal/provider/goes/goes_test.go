package goes

import (
	"testing"
	"time"
)

func TestParseGOESObjectTime(t *testing.T) {
	name := "OR_ABI-L1b-RadF-M6C02_G18_s20262650930215_e20262650939524_c20262650939553.nc"
	start, end, key, ok := parseGOESObjectTime(name)
	if !ok {
		t.Fatal("expected filename to parse")
	}
	if key != "20262650930215" {
		t.Fatalf("unexpected key %q", key)
	}
	wantStart := time.Date(2026, 9, 22, 9, 30, 21, 500_000_000, time.UTC)
	if !start.Equal(wantStart) {
		t.Fatalf("start=%s want=%s", start, wantStart)
	}
	wantEnd := time.Date(2026, 9, 22, 9, 39, 52, 400_000_000, time.UTC)
	if !end.Equal(wantEnd) {
		t.Fatalf("end=%s want=%s", end, wantEnd)
	}
}

func TestGOESCadence(t *testing.T) {
	cases := map[string]string{
		"ABI-L1b-RadF": "10m",
		"ABI-L1b-RadC": "5m",
		"ABI-L1b-RadM": "1m",
		"GLM-L2-LCFA":  "event",
	}
	for product, want := range cases {
		if got := goesCadence(product); got != want {
			t.Fatalf("%s cadence=%q want=%q", product, got, want)
		}
	}
}
