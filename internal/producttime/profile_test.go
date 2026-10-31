package producttime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestExplicitScientificSemantics(t *testing.T) {
	tests := []struct {
		source, code, kind, semantic, subtype string
		window                                int
	}{
		{"calipso", "C3880523239-LARC_CLOUD", "monthly", "statistics", "monthly", 0},
		{"calipso", "C3880518087-LARC_CLOUD", "orbit", "", "", 0},
		{"cloudsat", "3F-RMCP.R05_V0002", "observation", "statistics", "multi_period", 0},
		{"cloudsat", "3S-RMCP.R05_V0002", "observation", "statistics", "multi_period", 0},
		{"cloudsat", "2B-GEOPROF.P1_R05", "orbit", "", "", 0},
		{"goes18", "ABI-Flood-Day-TIF", "daily", "composite", "daily_flood", 1440},
		{"goes19", "ABI-Flood-Hourly-TIF", "observation", "composite", "hourly_flood", 60},
		{"modis", "C3903710663-LAADS", "observation", "composite", "multisensor_half_hour", 30},
		{"viirs", "C3348093425-LAADS", "observation", "composite", "multisensor_half_hour", 30},
		{"mtg", "EO:EUM:DAT:0686", "observation", "accumulation", "lightning", 0},
		{"mtg", "EO:EUM:DAT:0690", "observation", "event", "lightning", 0},
		{"goes16", "GLM-L2-LCFA", "observation", "event", "lightning", 0},
		{"goes16", "MAG-L1b-GEOF", "observation", "sensor", "magnetic_field", 0},
		{"earthcare", "AUX_MET_1D", "observation", "ancillary", "support", 0},
		{"cloudsat", "MODIS-AUX.P_R05", "orbit", "ancillary", "orbit_support", 0},
		{"calipso", "C3964848342-LARC_CLOUD", "orbit", "ancillary", "instrument_support", 0},
		{"era5", "reanalysis-era5-land", "observation", "reanalysis", "hourly", 0},
		{"goes18", "ABI-Flood-Hourly-unknown", "observation", "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.source+"/"+tc.code, func(t *testing.T) {
			p := Resolve(model.Product{SourceID: tc.source, ProductKey: tc.source + ":" + tc.code, Cadence: "event"})
			if p.Kind != tc.kind || p.Semantic != tc.semantic || p.Subtype != tc.subtype || p.WindowMinutes != tc.window {
				t.Fatalf("unexpected profile: %+v", p)
			}
			if tc.semantic != "" && p.SemanticLabel == "" {
				t.Fatal("missing semantic label")
			}
			if p.Kind != "scan" && (p.CadenceMinutes != 0 || p.SelectionMinutes != 0) {
				t.Fatalf("invented scan axis: %+v", p)
			}
			if tc.source == "cloudsat" && p.SelectionBasis != "file_start" {
				t.Fatalf("lost provider file-start constraint: %+v", p)
			}
		})
	}
}

func TestResolvedCALIPSOMonthlyBoundaries(t *testing.T) {
	p := Resolve(model.Product{SourceID: "calipso", ProductKey: "calipso:C3880523239-LARC_CLOUD", Cadence: "event"})
	start := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := p.ValidateRange(start, end); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateRange(start.AddDate(0, 0, 1), end); err == nil {
		t.Fatal("accepted partial month")
	}
	if err := p.ValidateTimes(nil, map[string][]string{"2024-02-01": {"00:00"}}); err == nil {
		t.Fatal("accepted minute scene for monthly product")
	}
}

func TestSnapshotProductKeysForExplicitRules(t *testing.T) {
	tests := []struct {
		source, key, dataType, cadence, semantic, subtype, label string
	}{
		{"earthcare", "earthcare:EarthCAREAuxiliary_MAAP:AUX_CCOG_B", "AUX_CCOG_B", "event", "ancillary", "support", "辅助资料"},
		{"earthcare", "earthcare:EarthCAREL1InstChecked_MAAP:AUX_JSG_1D", "AUX_JSG_1D", "event", "ancillary", "support", "辅助资料"},
		{"earthcare", "earthcare:EarthCAREXMETL1DProducts10_MAAP:AUX_MET_1D", "AUX_MET_1D", "event", "ancillary", "support", "辅助资料"},
		{"earthcare", "earthcare:EarthCAREOrbitData_MAAP:MPL_ORBSCT", "MPL_ORBSCT", "event", "ancillary", "support", "辅助资料"},
		{"era5", "era5:reanalysis-era5-land", "reanalysis-era5-land", "hourly", "reanalysis", "hourly", "再分析"},
		{"era5", "era5:reanalysis-era5-land-monthly-means", "reanalysis-era5-land-monthly-means", "monthly", "reanalysis", "monthly_mean", "月平均再分析"},
		{"modis", "modis:C3903710663-LAADS", "XAERDT_L3_MEASURES_QD_HH", "event", "composite", "multisensor_half_hour", "半小时合成"},
		{"mtg", "mtg:EO:EUM:DAT:0686", "EO:EUM:DAT:0686", "event", "accumulation", "lightning", "闪电累计"},
		{"goes18", "goes18:ABI-Flood-Hourly-TIF", "ABI-Flood-Hourly-TIF", "10m", "composite", "hourly_flood", "小时合成"},
		{"calipso", "calipso:C3880521656-LARC_CLOUD", "CAL_LID_L2_BlowingSnow_Antarctica-Standard-V1-00", "event", "", "", "吹雪资料"},
		{"calipso", "calipso:C3880703841-LARC_CLOUD", "GloSSAC", "event", "", "", "气溶胶气候资料"},
		{"calipso", "calipso:C3880518087-LARC_CLOUD", "CAL_IIR_L3_GEWEX_Cloud-Standard-V1-00", "event", "", "", "红外资料"},
		{"calipso", "calipso:C3880519029-LARC_CLOUD", "CAL_LID_L0-Standard-V1-00", "event", "", "", "激光雷达原始数据"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			p := Resolve(model.Product{SourceID: tc.source, ProductKey: tc.key, DataType: tc.dataType, Cadence: tc.cadence})
			if p.Semantic != tc.semantic || p.Subtype != tc.subtype || p.SemanticLabel != tc.label {
				t.Fatalf("unexpected profile: %+v", p)
			}
		})
	}
	unknown := Resolve(model.Product{SourceID: "earthcare", ProductKey: "earthcare:EarthCAREAuxiliary_MAAP:AUX_CCOG_B:other"})
	if unknown.Semantic != "" {
		t.Fatalf("matched an unrecognized code: %+v", unknown)
	}
}

func TestScientificFamilyLabelsDoNotImplyScanAxes(t *testing.T) {
	tests := []struct {
		source, key, cadence, label, semantic, subtype string
	}{
		{"modis", "modis:C1443531026-LAADS", "5m", "沿轨观测", "observation", "swath"},
		{"viirs", "viirs:C2105087273-LAADS", "6m", "沿轨观测", "observation", "swath"},
		{"earthcare", "earthcare:EarthCAREL2Products_MAAP:ACM_CAP_2B", "event", "沿轨观测", "observation", "orbit"},
		{"goes16", "goes16:ABI-L2-ACHAF", "10m", "反演产品", "derived", "retrieval"},
		{"goes18", "goes18:ABI-L2-DMWF", "10m", "运动风场", "derived", "motion_wind"},
		{"goes19", "goes19:ABI-L2-RRQPEF", "10m", "降水强度", "derived", "precipitation_rate"},
		{"goes16", "goes16:ABI-L2-MCMIPF", "10m", "多波段影像", "derived", "multiband_imagery"},
		{"goes17", "goes17:ABI-L2-LSAF", "10m", "反照率", "derived", "albedo"},
		{"goes16", "goes16:ABI-L1b-RadF-Reproc", "10m", "扫描时间", "scanning", "reprocessed"},
		{"himawari9", "himawari9:AHI-L1b-Japan", "2.5m", "区域扫描", "scanning", "regional"},
		{"mtg", "mtg:EO:EUM:DAT:0737", "event", "区域探测", "sounding", "regional"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			p := Resolve(model.Product{SourceID: tc.source, ProductKey: tc.key, Cadence: tc.cadence})
			if p.SemanticLabel != tc.label || p.Semantic != tc.semantic || p.Subtype != tc.subtype {
				t.Fatalf("unexpected profile: %+v", p)
			}
			if p.Kind == "scan" || p.SelectionMinutes != 0 || p.CadenceMinutes != 0 || p.WindowMinutes != 0 {
				t.Fatalf("scientific classification invented an axis: %+v", p)
			}
			if err := p.ValidateTimes(nil, map[string][]string{"2024-01-01": {"00:03"}}); err != nil {
				t.Fatalf("sparse scene rejected: %v", err)
			}
		})
	}
	for _, p := range []model.Product{
		{SourceID: "goes18", ProductKey: "goes18:ABI-L2-UNRECOGNIZEDF", Cadence: "10m"},
		{SourceID: "modis", ProductKey: "modis:C0000000000-LAADS", Cadence: "5m"},
		{SourceID: "earthcare", ProductKey: "earthcare:EarthCAREL2Products_MAAP:UNKNOWN_2A", Cadence: "event"},
	} {
		got := Resolve(p)
		if got.Semantic != "" || got.SemanticLabel != "" || got.Kind != "observation" {
			t.Fatalf("unknown collection classified: %+v", got)
		}
	}
}

func TestRemainingScientificCollectionsPreserveScheduling(t *testing.T) {
	tests := []struct {
		source, key, semantic, subtype, label string
	}{
		{"earthcare", "earthcare:EarthCAREL1InstChecked_MAAP:MSI_TRF_1B", "ancillary", "calibration", "校准资料"},
		{"modis", "modis:C1444624388-LAADS", "observation", "station_subset", "站点观测子集"},
		{"viirs", "viirs:C3860041042-LAADS", "derived", "cloud_mask", "云掩膜和光谱测试"},
		{"himawari8", "himawari8:AHI-L2-FLDK-SST", "derived", "sea_surface_temperature", "海表温度"},
		{"himawari9", "himawari9:AHI-L2-FLDK-Winds", "derived", "motion_wind", "大气运动风"},
		{"mtg", "mtg:EO:EUM:DAT:0682", "derived", "fire_monitoring", "火灾监测"},
		{"mtg", "mtg:EO:EUM:DAT:1086", "derived", "precipitation_rate", "降水强度"},
		{"mtg", "mtg:EO:EUM:DAT:0694", "derived", "sea_surface_temperature", "海表温度"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			p := model.Product{SourceID: tc.source, ProductKey: tc.key, Cadence: "10m"}
			got, before := Resolve(p), resolveSchedule(p)
			if got.Semantic != tc.semantic || got.Subtype != tc.subtype || got.SemanticLabel != tc.label {
				t.Fatalf("unexpected semantics: %+v", got)
			}
			if got.Kind != before.Kind || got.CadenceMinutes != before.CadenceMinutes || got.SelectionMinutes != before.SelectionMinutes || got.SelectionBasis != before.SelectionBasis || got.Days != before.Days || got.WindowMinutes != 0 {
				t.Fatalf("schedule changed: before %+v after %+v", before, got)
			}
		})
	}
	for _, source := range []string{"himawari8", "himawari9"} {
		got := Resolve(model.Product{SourceID: source, ProductKey: source + ":AHI-L2-FLDK-ISatSS", Cadence: "10m"})
		if got.Semantic != "" || got.SemanticLabel != "" || got.Kind != "observation" {
			t.Fatalf("unverified ISatSS classified: %+v", got)
		}
	}
}

func TestScientificWindowsDoNotCreateScanAxes(t *testing.T) {
	for _, p := range []model.Product{
		{SourceID: "goes18", ProductKey: "goes18:ABI-Flood-Hourly-TIF", Cadence: "10m"},
		{SourceID: "modis", ProductKey: "modis:C3903710663-LAADS", Cadence: "event"},
	} {
		profile := Resolve(p)
		if err := profile.ValidateTimes([]string{"00:30"}, nil); err == nil {
			t.Fatalf("accepted recurring scan for %+v", profile)
		}
		if err := profile.ValidateTimes(nil, map[string][]string{"2024-01-01": {"00:03"}}); err != nil {
			t.Fatalf("rejected sparse scene: %v", err)
		}
	}
	b, err := json.Marshal(Resolve(model.Product{SourceID: "other"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "semantic") || strings.Contains(string(b), "window_minutes") {
		t.Fatalf("unknown metadata should be omitted: %s", b)
	}
}

func TestEventScenesAllowSecondsWithoutCreatingScanAxis(t *testing.T) {
	p := Resolve(model.Product{SourceID: "viirs", ProductKey: "viirs:C3860041042-LAADS"})
	if err := p.ValidateTimes(nil, map[string][]string{"2024-01-01": {"00:02:30"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateTimes([]string{"00:02:30"}, nil); err == nil {
		t.Fatal("event product accepted recurring times")
	}
}

func TestProductTemporalSemantics(t *testing.T) {
	for _, tc := range []struct {
		product model.Product
		kind    string
	}{
		{model.Product{SourceID: "goes18", ProductKey: "goes18:ABI-L1b-RadF", Cadence: "10m"}, "scan"},
		{model.Product{SourceID: "himawari9", ProductKey: "himawari9:AHI-L1b-FLDK", Cadence: "10m"}, "scan"},
		{model.Product{SourceID: "mtg", ProductKey: "mtg:EO:EUM:DAT:0662", Cadence: "10m"}, "scan"},
		{model.Product{SourceID: "mtg", ProductKey: "mtg:EO:EUM:DAT:0801", Cadence: "10m"}, "observation"},
		{model.Product{SourceID: "goes18", ProductKey: "goes18:otherF", Cadence: "10m"}, "observation"},
		{model.Product{SourceID: "modis", Cadence: "5m", DisplayName: "MODIS 5-Min L1A Swath"}, "observation"},
		{model.Product{SourceID: "viirs", Cadence: "6m"}, "observation"},
		{model.Product{SourceID: "cloudsat", Cadence: "event"}, "orbit"},
		{model.Product{SourceID: "earthcare", AreaType: "ORBIT"}, "orbit"},
		{model.Product{Cadence: "event", DisplayName: "Eight day rolling composite Daily L3"}, "daily"},
		{model.Product{Cadence: "event", DisplayName: "MODIS 16-Day L3"}, "composite"},
		{model.Product{Cadence: "8d"}, "composite"},
		{model.Product{Cadence: "10d"}, "composite"},
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
		{"annual", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z", true},
		{"annual", "2024-02-01T00:00:00Z", "2025-01-01T00:00:00Z", false},
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
