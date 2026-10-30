package producttime

import (
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestProductTemporalSemantics(t *testing.T) {
	for _, tc := range []struct {
		product model.Product
		kind    string
	}{
		{model.Product{SourceID: "goes18", ProductKey: "goes18:ABI-L1b-RadF", Cadence: "10m"}, "scan"},
		{model.Product{SourceID: "himawari9", ProductKey: "himawari9:AHI-L1b-FLDK", Cadence: "10m"}, "scan"},
		{model.Product{SourceID: "modis", Cadence: "5m", DisplayName: "MODIS 5-Min L1A Swath"}, "observation"},
		{model.Product{SourceID: "viirs", Cadence: "6m"}, "observation"},
		{model.Product{SourceID: "cloudsat", Cadence: "event"}, "orbit"},
		{model.Product{SourceID: "earthcare", AreaType: "ORBIT"}, "orbit"},
		{model.Product{Cadence: "event", DisplayName: "Eight day rolling composite Daily L3"}, "daily"},
		{model.Product{Cadence: "event", DisplayName: "MODIS 16-Day L3"}, "composite"},
		{model.Product{Cadence: "8d"}, "composite"},
		{model.Product{Cadence: "10d"}, "composite"}, // ten days is not a calendar dekad
		{model.Product{Cadence: "event", DisplayName: "Weekly mean"}, "weekly"},
		{model.Product{Cadence: "dekad"}, "dekadal"},
		{model.Product{Cadence: "1mo"}, "monthly"},
		{model.Product{DisplayName: "Annual mean"}, "annual"},
		{model.Product{DataType: "AUX_JSG_1D", Cadence: "event"}, "observation"}, // 1D is not daily
	} {
		if got := Resolve(tc.product); got.Kind != tc.kind {
			t.Errorf("%+v: got %+v want %s", tc.product, got, tc.kind)
		}
	}
}

func TestCalendarBoundariesAndUTC(t *testing.T) {
	for _, tc := range []struct {
		kind, start, end string
		valid            bool
	}{
		{"monthly", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z", true},
		{"monthly", "2024-02-02T00:00:00Z", "2024-03-01T00:00:00Z", false},
		{"daily", "2024-02-29T00:00:00+08:00", "2024-03-01T00:00:00+08:00", false},
		{"dekadal", "2024-02-21T00:00:00Z", "2024-03-01T00:00:00Z", true},
		{"dekadal", "2024-02-20T00:00:00Z", "2024-03-01T00:00:00Z", false},
		{"annual", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z", true},
		{"annual", "2024-02-01T00:00:00Z", "2025-01-01T00:00:00Z", false},
		{"weekly", "2024-02-03T00:00:00Z", "2024-02-04T00:00:00Z", true}, // upstream anchor is not assumed to be Monday
		{"composite", "2024-12-31T00:00:00Z", "2025-01-01T00:00:00Z", true},
	} {
		start, _ := time.Parse(time.RFC3339, tc.start)
		end, _ := time.Parse(time.RFC3339, tc.end)
		if err := (Profile{Kind: tc.kind}).ValidateRange(start, end); (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestCoverageCrossesRequestStartAndIsOwnedOnce(t *testing.T) {
	parse := func(value string) time.Time {
		at, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	start := parse("2024-12-31T00:00:00Z")
	prior := parse("2024-12-27T00:00:00Z")
	end := parse("2025-01-04T00:00:00Z")
	if !OwnsCoverage(prior, end, start, start, start.AddDate(0, 0, 1)) {
		t.Fatal("cross-year composite was lost at request boundary")
	}
	if OwnsCoverage(prior, end, start, start.AddDate(0, 0, 1), start.AddDate(0, 0, 2)) {
		t.Fatal("composite counted again in next chunk")
	}
	if OwnsCoverage(prior, start, start, start, start.AddDate(0, 0, 1)) {
		t.Fatal("touching exclusive boundary counted as overlap")
	}
	if OwnsCoverage(end, time.Time{}, start, start, end) {
		t.Fatal("exclusive end included")
	}
	if !OwnsCoverage(start, time.Time{}, start, start, end) {
		t.Fatal("point event lost")
	}
}
