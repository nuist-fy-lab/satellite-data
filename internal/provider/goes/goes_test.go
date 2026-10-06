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

func TestArchiveIdentity(t *testing.T) {
	for prefix, want := range map[string]string{
		"OR_ABI-L1b-RadF-M6C02_G18":  "goes18:ABI-L1b-RadF",
		"OR_ABI-L1b-RadC-M6C14_G16":  "goes16:ABI-L1b-RadC",
		"OR_ABI-L1b-RadM1-M6C06_G19": "goes19:ABI-L1b-RadM",
		"OR_ABI-L2-CMIPM2-M6C06_G17": "goes17:ABI-L2-CMIPM",
		"OR_GLM-L2-LCFA_G18":         "goes18:GLM-L2-LCFA",
		"OR_SUVI-L1b-Fe171_G18":      "goes18:SUVI-L1b-Fe171",
	} {
		name := prefix + "_s20262650930215_e20262650939524_c20262650939553.nc"
		product, start, ok := ArchiveIdentity(name)
		if !ok || product != want || start.Format(time.DateOnly) != "2026-09-22" {
			t.Fatalf("%s: product=%q start=%s ok=%v", name, product, start, ok)
		}
	}
	for _, name := range []string{
		"OR_ABI-L1b-RadF-M6C02_G18_s20263670930215_e20262650939524_c20262650939553.nc",
		"OR_ABI-L1b-RadF-M6C02_G18_s20263660930215_e20262650939524_c20262650939553.nc",
		"OR_ABI-L1b-RadF-M6C02_G20_s20262650930215_e20262650939524_c20262650939553.nc",
		"OR_ABI-L1b-RadF-M6C02_G18_s20262650930215_e20262650939524_c20262650939553.nc.part",
		"not-goes.nc",
	} {
		if _, _, ok := ArchiveIdentity(name); ok {
			t.Fatalf("unexpected identity for %s", name)
		}
	}
}
