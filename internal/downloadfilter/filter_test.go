package downloadfilter

import (
	"reflect"
	"testing"
)

func TestGOESBandAndHourlySelection(t *testing.T) {
	profile := ForProduct("goes18", "goes18:ABI-L1b-RadF")
	if profile == nil || len(profile.Bands) != 16 || profile.Bands[5].Number != 6 || profile.Bands[5].Resolution != "2 km" {
		t.Fatal("GOES ABI profile not loaded")
	}
	s, err := (Selection{Band: 6, Times: []string{"12:00", "00:00", "12:00"}}).Normalize("goes18", "goes18:ABI-L1b-RadF")
	if err != nil || !reflect.DeepEqual(s.Times, []string{"00:00", "12:00"}) {
		t.Fatalf("selection=%+v err=%v", s, err)
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"OR_ABI-L1b-RadF-M6C06_G18_s20231520000203_e20231520009511_c20231520010000.nc", true},
		{"OR_ABI-L1b-RadF-M6C07_G18_s20231520000203_e20231520009511_c20231520010000.nc", false},
	} {
		if got := s.MatchesAsset("goes18", "goes18:ABI-L1b-RadF", tc.name); got != tc.want {
			t.Errorf("%s matched=%v", tc.name, got)
		}
	}
}

func TestSelectionValidationAndOtherProviders(t *testing.T) {
	for _, s := range []Selection{{Band: 6}, {Times: []string{"25:00"}}} {
		if _, err := s.Normalize("viirs", "viirs:test"); err == nil {
			t.Fatalf("accepted invalid selection %+v", s)
		}
	}
	if _, err := (Selection{Times: []string{"12:00"}}).Normalize("viirs", "viirs:test"); err != nil {
		t.Fatal(err)
	}
	if ForProduct("modis", "modis:test") != nil {
		t.Fatal("unexpected MODIS band profile")
	}
	hs, err := (Selection{Band: 3}).Normalize("himawari9", "himawari9:AHI-L1b-FLDK")
	if err != nil || !hs.MatchesAsset("himawari9", "himawari9:AHI-L1b-FLDK", "HS_H09_20260922_0500_B03_FLDK_R05_S0110.DAT.bz2") || hs.MatchesAsset("himawari9", "himawari9:AHI-L1b-FLDK", "HS_H09_20260922_0500_B04_FLDK_R05_S0110.DAT.bz2") {
		t.Fatalf("Himawari band selection=%+v err=%v", hs, err)
	}
}
