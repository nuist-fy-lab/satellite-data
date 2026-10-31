package goes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/objectstore"
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
		"MAG-L1b-GEOF": "event",
	}
	for product, want := range cases {
		if got := goesCadence(product); got != want {
			t.Fatalf("%s cadence=%q want=%q", product, got, want)
		}
	}
	if got := goesArea("MAG-L1b-GEOF"); got != "" {
		t.Fatalf("MAG area=%q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchOverlappingFilesAndScanModes(t *testing.T) {
	product := "ABI-L1b-RadF"
	prior := product + "/2026/001/23/"
	current := product + "/2026/002/00/"
	files := map[string][]string{
		prior: {
			"OR_ABI-L1b-RadF-M3C01_G18_s20260012350000_e20260020001000_c20260020002000.nc",
			"OR_ABI-L1b-RadF-M3C02_G18_s20260012350000_e20260020001000_c20260020002000.nc",
			"OR_ABI-L1b-RadF-M6C01_G18_s20260012340000_e20260020000000_c20260020001000.nc",
		},
		current: {
			"OR_ABI-L1b-RadF-M4C01_G18_s20260020001000_e20260020005000_c20260020006000.nc",
			"OR_ABI-L1b-RadF-M6C01_G18_s20260020006000_e20260020009000_c20260020010000.nc",
			"OR_ABI-L1b-RadF-M6C01_G18_s20260020006000_e20260020009000_c20260020010000.nc",
			"OR_ABI-L1b-RadF-M6C01_G18_s20260020010000_e20260020015000_c20260020016000.nc",
		},
	}
	requested := map[string]bool{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		prefix := r.URL.Query().Get("prefix")
		requested[prefix] = true
		items := make([]map[string]string, 0)
		for _, name := range files[prefix] {
			items = append(items, map[string]string{"name": prefix + name})
		}
		body, _ := json.Marshal(map[string]any{"items": items})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	p, err := New(18, objectstore.NewClient(client))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "goes18:" + product, Start: start, End: start.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !requested[prior] || !requested[current] {
		t.Fatalf("requested prefixes: %v", requested)
	}
	if len(got) != 3 {
		t.Fatalf("granules=%+v", got)
	}
	if got[0].Metadata["scan_mode"] != "3" || len(got[0].Assets) != 2 || !got[0].End.Equal(start.Add(time.Minute)) {
		t.Fatalf("overlapping granule=%+v", got[0])
	}
	if got[1].Metadata["scan_mode"] != "4" {
		t.Fatalf("second granule=%+v", got[1])
	}
	if got[2].Metadata["scan_mode"] != "6" || len(got[2].Assets) != 1 {
		t.Fatalf("duplicate or mode error: %+v", got[2])
	}
}

func TestSearchFloodCalendarPathsAndDistinctComposites(t *testing.T) {
	product := "ABI-Flood-Hourly"
	prefix := product + "/2025/08/25/"
	names := []string{
		"ABI-Flood-HCOM-AOI002_v1r1_g19_s202508251140210_e202508251549519_c202508251610025.nc",
		"ABI-Flood-HCOM-AOI002_v1r1_g19_s202508251140210_e202508251649519_c202508251710024.nc",
		"ABI-Flood-HCOM-AOI002_v1r1_g19_s202508251140210_e202508251649519_c202508251720024.nc",
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		data := map[string]any{}
		if r.URL.Query().Get("prefix") == prefix {
			if r.URL.Query().Get("pageToken") == "" {
				data["nextPageToken"] = "second"
				data["items"] = []map[string]string{{"name": prefix + names[0]}}
			} else {
				data["items"] = []map[string]string{{"name": prefix + names[0]}, {"name": prefix + names[1]}, {"name": prefix + names[2]}}
			}
		}
		body, _ := json.Marshal(data)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	p, _ := New(19, objectstore.NewClient(client))
	start := time.Date(2025, 8, 25, 15, 0, 0, 0, time.UTC)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "goes19:" + product, Start: start, End: start.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[0].Assets) != 1 || len(got[1].Assets) != 1 || len(got[2].Assets) != 1 {
		t.Fatalf("granules=%+v", got)
	}
	if !got[0].Start.Equal(time.Date(2025, 8, 25, 11, 40, 21, 0, time.UTC)) || !got[0].End.Equal(time.Date(2025, 8, 25, 15, 49, 51, 900000000, time.UTC)) {
		t.Fatalf("granule=%+v", got[0])
	}
	got, err = p.Search(context.Background(), model.Query{CollectionID: "goes19:" + product, Start: time.Date(2025, 8, 25, 15, 49, 51, 900000000, time.UTC), End: time.Date(2025, 8, 25, 15, 50, 0, 0, time.UTC)})
	if err != nil || len(got) != 2 {
		t.Fatalf("exclusive end: granules=%+v err=%v", got, err)
	}
	got, err = p.Search(context.Background(), model.Query{CollectionID: "goes19:" + product, Start: time.Date(2025, 8, 25, 11, 39, 0, 0, time.UTC), End: time.Date(2025, 8, 25, 11, 40, 21, 0, time.UTC)})
	if err != nil || len(got) != 0 {
		t.Fatalf("exclusive query end: granules=%+v err=%v", got, err)
	}
}

func TestSearchFloodYearBoundaryAndMedia(t *testing.T) {
	for _, tc := range []struct{ product, extension, media string }{
		{"ABI-Flood-Day", ".nc", "application/x-netcdf"},
		{"ABI-Flood-Day-TIF", ".tif", "image/tiff"},
		{"ABI-Flood-Day-Shapefiles", ".zip", "application/zip"},
	} {
		t.Run(tc.product, func(t *testing.T) {
			prior := tc.product + "/2025/12/31/"
			requested := map[string]bool{}
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				prefix := r.URL.Query().Get("prefix")
				requested[prefix] = true
				data := map[string]any{}
				if prefix == prior {
					data["items"] = []map[string]string{{"name": prior + "ABI-Flood-DCOM-AOI002_v1r1_g19_s202512312300000_e202601010100000_c202601010200000" + tc.extension}}
				}
				body, _ := json.Marshal(data)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			p, _ := New(19, objectstore.NewClient(client))
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			got, err := p.Search(context.Background(), model.Query{CollectionID: "goes19:" + tc.product, Start: start, End: start.Add(time.Minute)})
			if err != nil || len(got) != 1 || got[0].Assets[0].MediaType != tc.media || !requested[prior] || !requested[tc.product+"/2026/01/01/"] {
				t.Fatalf("granules=%+v requested=%v err=%v", got, requested, err)
			}
		})
	}
}

func TestSearchFloodLeapDayBoundary(t *testing.T) {
	product := "ABI-Flood-Day"
	prefix := product + "/2024/02/29/"
	requested := map[string]bool{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.Query().Get("prefix")
		requested[path] = true
		data := map[string]any{}
		if path == prefix {
			data["items"] = []map[string]string{{"name": prefix + "ABI-Flood-DCOM-AOI002_v1r1_g19_s202402292300000_e202403010100000_c202403010200000.nc"}}
		}
		body, _ := json.Marshal(data)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	p, _ := New(19, objectstore.NewClient(client))
	start := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := p.Search(context.Background(), model.Query{CollectionID: "goes19:" + product, Start: start, End: start.Add(time.Minute)})
	if err != nil || len(got) != 1 || !requested[prefix] || !requested[product+"/2024/03/01/"] {
		t.Fatalf("granules=%+v requested=%v err=%v", got, requested, err)
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
