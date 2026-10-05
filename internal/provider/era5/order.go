package era5

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Order inputs describe data only. Dates and CDS request batching belong to the centre.
func ValidateOrder(collection string, start, end time.Time, inputs map[string]json.RawMessage) error {
	if _, err := datasetID(collection); err != nil {
		return err
	}
	if !end.After(start) || start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 || end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0 || end.Nanosecond() != 0 {
		return fmt.Errorf("%w: ERA5 orders require whole UTC dates", ErrInvalidRequest)
	}
	if strings.Contains(collection, "monthly-means") && (start.Day() != 1 || end.Day() != 1) {
		return fmt.Errorf("%w: monthly products require complete UTC months", ErrInvalidRequest)
	}
	allowed := map[string]bool{"variable": true, "pressure_level": true, "product_type": true, "area": true, "time": true, "data_format": true, "download_format": true}
	for key := range inputs {
		if !allowed[key] {
			return fmt.Errorf("%w: unsupported order input; dates are supplied with start/end", ErrInvalidRequest)
		}
	}
	var variables []string
	if json.Unmarshal(inputs["variable"], &variables) != nil || len(variables) == 0 {
		return fmt.Errorf("%w: at least one variable is required", ErrInvalidRequest)
	}
	for _, value := range variables {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: empty variable", ErrInvalidRequest)
		}
	}
	if strings.Contains(collection, "pressure-levels") {
		var levels []string
		if json.Unmarshal(inputs["pressure_level"], &levels) != nil || len(levels) == 0 {
			return fmt.Errorf("%w: pressure levels are required", ErrInvalidRequest)
		}
	}
	if raw, ok := inputs["area"]; ok {
		var area []float64
		if json.Unmarshal(raw, &area) != nil || len(area) != 4 {
			return fmt.Errorf("%w: area must be [north,west,south,east]", ErrInvalidRequest)
		}
		for _, value := range area {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%w: invalid area", ErrInvalidRequest)
			}
		}
		if area[0] < area[2] || area[1] > area[3] || area[0] > 90 || area[2] < -90 || area[1] < -180 || area[3] > 180 {
			return fmt.Errorf("%w: invalid area bounds", ErrInvalidRequest)
		}
	}
	return nil
}

func PlanOrderPart(collection string, start, end time.Time, base map[string]json.RawMessage, process Process) (Submission, time.Time, error) {
	if err := ValidateOrder(collection, start, end, base); err != nil {
		return Submission{}, time.Time{}, err
	}
	next := start.AddDate(0, 0, 1)
	monthly := strings.Contains(collection, "monthly-means")
	if monthly {
		next = start.AddDate(0, 1, 0)
	}
	if next.After(end) {
		next = end
	}
	inputs := make(map[string]json.RawMessage, len(base)+7)
	for key, value := range base {
		if _, ok := process.Inputs[key]; !ok {
			return Submission{}, time.Time{}, fmt.Errorf("%w: input is unavailable for this dataset", ErrInvalidRequest)
		}
		inputs[key] = value
	}
	put := func(key string, value any) { inputs[key], _ = json.Marshal(value) }
	date := func(key, value string) {
		var field struct {
			Schema struct {
				Type string `json:"type"`
			} `json:"schema"`
		}
		_ = json.Unmarshal(process.Inputs[key], &field)
		if field.Schema.Type == "string" {
			put(key, value)
		} else {
			put(key, []string{value})
		}
	}
	date("year", start.Format("2006"))
	date("month", start.Format("01"))
	if !monthly {
		date("day", start.Format("02"))
	}
	if _, ok := inputs["time"]; !ok {
		times := []string{"00:00"}
		if !monthly {
			for hour := 1; hour < 24; hour++ {
				times = append(times, fmt.Sprintf("%02d:00", hour))
			}
		}
		put("time", times)
	}
	if _, ok := process.Inputs["product_type"]; ok {
		if _, set := inputs["product_type"]; !set {
			value := "reanalysis"
			if monthly {
				value = "monthly_averaged_reanalysis"
			}
			put("product_type", []string{value})
		}
	}
	if _, ok := inputs["data_format"]; !ok {
		put("data_format", "netcdf")
	}
	if _, ok := inputs["download_format"]; !ok {
		put("download_format", "unarchived")
	}
	return Submission{CollectionID: collection, Inputs: inputs}, next, nil
}
