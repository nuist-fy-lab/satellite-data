package slotaxis

import (
	"testing"
	"time"
)

func at(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestFixedAxisUsesNominalSlotsAndAdaptiveTolerance(t *testing.T) {
	axis := Resolve("goes18", "goes18:ABI-L1b-RadF", "10m")
	observed := []time.Time{at("2023-06-01T00:01:20Z"), at("2023-06-01T00:11:22Z"), at("2023-06-01T00:21:19Z")}
	if axis.Mode() != "grid" || axis.Classify(observed) != "grid" || axis.Tolerance(observed) < 80*time.Second {
		t.Fatalf("axis=%+v tolerance=%v", axis, axis.Tolerance(observed))
	}
	nominal, ok := axis.Match(observed[0], []string{"00:00", "01:00"}, observed)
	if !ok || nominal != at("2023-06-01T00:00:00Z") {
		t.Fatalf("fixed nominal=%s ok=%v", nominal, ok)
	}
	if _, ok := axis.Match(observed[1], []string{"00:00"}, observed); ok {
		t.Fatal("adjacent scan matched hourly slot")
	}
	if axis.Tolerance([]time.Time{at("2023-06-01T00:04:59Z"), observed[1], observed[2]}) >= 5*time.Minute {
		t.Fatal("tolerance crosses half a scan period")
	}
	if axis.Classify([]time.Time{observed[0], observed[1], at("2023-06-01T00:24:30Z")}) != "events" {
		t.Fatal("off-grid scan forced into fixed axis")
	}
}

func TestCrossMidnightAndEventIsolation(t *testing.T) {
	axis := Resolve("goes18", "goes18:ABI-L1b-RadF", "10m")
	scan := at("2023-05-31T23:59:40Z")
	nominal, ok := axis.Match(scan, []string{"00:00"}, []time.Time{scan})
	if !ok || nominal != at("2023-06-01T00:00:00Z") {
		t.Fatalf("cross-midnight %s %v", nominal, ok)
	}
	event := Resolve("calipso", "calipso:test", "event")
	if event.Mode() != "events" {
		t.Fatalf("unexpected event mode %s", event.Mode())
	}
	near := []time.Time{at("2023-06-01T00:00:10Z"), at("2023-06-01T00:00:20Z")}
	if _, ok := event.Match(near[0], []string{"00:00"}, near); ok {
		t.Fatal("close event claimed ambiguous nominal slot")
	}
	isolated := []time.Time{at("2023-06-01T00:00:20Z"), at("2023-06-01T00:10:00Z")}
	if nominal, ok := event.Match(isolated[0], []string{"00:00"}, isolated); !ok || nominal != at("2023-06-01T00:00:00Z") {
		t.Fatalf("isolated event %s %v", nominal, ok)
	}
}

func TestGOESFileModeKeepsHistoricalScanTimes(t *testing.T) {
	for _, product := range []string{"ABI-L1b-RadF", "ABI-L2-CMIPF"} {
		base := Resolve("goes16", "goes16:"+product, "10m")
		for _, tc := range []struct {
			mode, actual, selected string
			cadence                int
		}{
			{"3", "2018-01-01T00:15:21Z", "00:15", 15},
			{"4", "2018-01-01T00:05:21Z", "00:05", 5},
			{"6", "2024-01-01T00:10:21Z", "00:10", 10},
		} {
			axis := base.WithGOESMode("goes16", "goes16:"+product, tc.mode)
			observed := []time.Time{at(tc.actual)}
			if axis.CadenceMinutes != tc.cadence {
				t.Fatalf("mode %s: %+v", tc.mode, axis)
			}
			if nominal, ok := axis.Match(observed[0], []string{tc.selected}, observed); !ok || nominal.Format("15:04") != tc.selected {
				t.Fatalf("mode %s lost %s: %v %v", tc.mode, tc.selected, nominal, ok)
			}
		}
		if base.WithGOESMode("goes16", "goes16:"+product, "").CadenceMinutes != 0 {
			t.Fatal("unknown mode forced to current grid")
		}
	}
}
