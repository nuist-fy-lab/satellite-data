package era5

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOrderPlansEveryDatasetWithoutClientScheduling(t *testing.T) {
	first := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	base := map[string]json.RawMessage{"variable": json.RawMessage(`["temperature"]`), "pressure_level": json.RawMessage(`["850"]`)}
	for _, dataset := range datasets {
		t.Run(dataset.id, func(t *testing.T) {
			inputs := map[string]json.RawMessage{}
			for _, key := range []string{"variable", "year", "month", "day", "time", "pressure_level", "product_type", "area", "data_format", "download_format"} {
				inputs[key] = json.RawMessage(`{"schema":{"type":"array"}}`)
			}
			if dataset.id == "reanalysis-era5-land" {
				inputs["year"] = json.RawMessage(`{"schema":{"type":"string"}}`)
				inputs["month"] = inputs["year"]
			}
			part, next, err := PlanOrderPart("era5:"+dataset.id, first, first.AddDate(0, 2, 0), base, Process{ID: dataset.id, Inputs: inputs})
			if err != nil {
				t.Fatal(err)
			}
			if part.CollectionID != "era5:"+dataset.id || string(part.Inputs["data_format"]) != `"netcdf"` {
				t.Fatalf("bad plan: %+v", part)
			}
			if dataset.cadence == "monthly" {
				if !next.Equal(first.AddDate(0, 1, 0)) || part.Inputs["day"] != nil {
					t.Fatal("monthly plan created daily dates")
				}
			} else if !next.Equal(first.AddDate(0, 0, 1)) {
				t.Fatal("hourly product not split by UTC day")
			}
			if dataset.id == "reanalysis-era5-land" && string(part.Inputs["year"]) != `"2024"` {
				t.Fatal("ERA5-Land scalar year schema ignored")
			}
		})
	}
}

func TestOrderRejectsSecretAndAmbiguousDateInputs(t *testing.T) {
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, inputs := range []map[string]json.RawMessage{
		{"variable": json.RawMessage(`["2m_temperature"]`), "token": json.RawMessage(`"fixture"`)},
		{"variable": json.RawMessage(`["2m_temperature"]`), "year": json.RawMessage(`["2024"]`)},
		{"variable": json.RawMessage(`["2m_temperature"]`), "area": json.RawMessage(`[10,110,20,100]`)},
		{"variable": json.RawMessage(`[]`)},
	} {
		if err := ValidateOrder("era5:reanalysis-era5-single-levels", first, first.AddDate(0, 0, 1), inputs); err == nil {
			t.Fatal("accepted invalid order inputs")
		}
	}
}
