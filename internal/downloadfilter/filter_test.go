package downloadfilter

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestSceneSecondsAndDailyLimit(t *testing.T) {
	times := make([]string, 576)
	for i := range times {
		seconds := i * 150
		times[i] = fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
	}
	times = append(times, "00:00")
	got, err := (Selection{Scenes: map[string][]string{"2024-01-01": times}}).Normalize("viirs", "viirs:test")
	if err != nil || len(got.Scenes["2024-01-01"]) != 576 || got.Scenes["2024-01-01"][0] != "00:00" || got.Scenes["2024-01-01"][1] != "00:02:30" {
		t.Fatalf("seconds or deduplication: %+v, %v", got, err)
	}
	if _, err := (Selection{Scenes: map[string][]string{"2024-01-01": make([]string, 1441)}}).Normalize("viirs", "viirs:test"); err == nil {
		t.Fatal("accepted excessive scene count")
	}
}

func TestGranuleSelectionNormalization(t *testing.T) {
	s, err := (Selection{GranuleIDs: []string{"b", "a", "b"}}).Normalize("viirs", "viirs:test")
	if err != nil || !reflect.DeepEqual(s.GranuleIDs, []string{"a", "b"}) {
		t.Fatalf("selection=%+v err=%v", s, err)
	}
	for _, s := range []Selection{{GranuleIDs: []string{"a"}, Times: []string{"00:00"}}, {GranuleIDs: []string{"a"}, Scenes: map[string][]string{"2024-01-01": {"00:00"}}}, {GranuleIDs: []string{" a"}}} {
		if _, err := s.Normalize("viirs", "viirs:test"); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestSparseResearchScenesPreserveExactObservations(t *testing.T) {
	selection, err := (Selection{Scenes: map[string][]string{
		"2006-07-23": {"21:45", "21:45", "21:50"},
		"2020-02-29": {"09:30"},
	}}).Normalize("modis", "modis:C1379758607-LAADS")
	if err != nil {
		t.Fatal(err)
	}
	if got := selection.Scenes["2006-07-23"]; !reflect.DeepEqual(got, []string{"21:45", "21:50"}) {
		t.Fatalf("lost or duplicated exact research scenes: %v", got)
	}
	start := time.Date(2006, 7, 23, 0, 0, 0, 0, time.UTC)
	end := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	day, ok := selection.NextSceneDay(start, end)
	if !ok || !day.Equal(start) {
		t.Fatalf("first scene day=%s ok=%v", day, ok)
	}
	day, ok = selection.NextSceneDay(start.AddDate(0, 0, 1), end)
	if !ok || day.Format(time.DateOnly) != "2020-02-29" {
		t.Fatalf("leap-day scene lost: day=%s ok=%v", day, ok)
	}
	day, ok = selection.NextSceneDay(day.AddDate(0, 0, 1), end)
	if ok || !day.IsZero() {
		t.Fatalf("invented extra research scene: day=%s ok=%v", day, ok)
	}
	if _, present := selection.TimesForDay("2010-01-01"); present {
		t.Fatal("unlisted research day was selected")
	}
}

func TestSparseResearchSceneValidation(t *testing.T) {
	for _, selection := range []Selection{
		{Times: []string{"00:00"}, Scenes: map[string][]string{"2020-02-29": {"00:05"}}},
		{Scenes: map[string][]string{"2020-02-30": {"00:05"}}},
		{Scenes: map[string][]string{"2020-02-29": {"24:00"}}},
		{Scenes: map[string][]string{"2020-02-29": {}}},
	} {
		if _, err := selection.Normalize("modis", "modis:C1379758607-LAADS"); err == nil {
			t.Fatalf("accepted invalid research selection: %+v", selection)
		}
	}
}

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

func TestBandForAsset(t *testing.T) {
	for _, tc := range []struct {
		source, product, filename string
		want                      int
	}{
		{"goes18", "goes18:ABI-L1b-RadF", "OR_ABI-L1b-RadF-M6C06_G18_s20231650000210.nc", 6},
		{"goes18", "goes18:ABI-L1b-RadF", "OR_ABI-L1b-RadF-M6C16_G18_s20231650000210.nc", 16},
		{"goes18", "goes18:ABI-L1b-RadF", "OR_ABI-L1b-RadF-M6C17_G18_s20231650000210.nc", 0},
		{"himawari9", "himawari9:AHI-L1b-FLDK", "HS_H09_20230926_0100_B03_FLDK.dat", 3},
		{"himawari9", "himawari9:AHI-L1b-FLDK", "not-a-band.dat", 0},
	} {
		profile := ForProduct(tc.source, tc.product)
		if got := profile.BandForAsset(tc.filename); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.filename, got, tc.want)
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
