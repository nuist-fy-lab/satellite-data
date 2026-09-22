package laads

import (
	"testing"
	"time"
)

func TestObjectList(t *testing.T) {
	raw := map[string]any{
		"content": []any{
			map[string]any{"product": "MOD021KM"},
			map[string]any{"product": "MOD02HKM"},
		},
	}
	got := objectList(raw)
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestFormatRange(t *testing.T) {
	start := time.Date(2026, 9, 20, 1, 2, 30, 0, time.UTC)
	end := time.Date(2026, 9, 20, 2, 3, 1, 0, time.UTC)
	if got, want := formatRange(start, end), "2026-09-20 01:02..2026-09-20 02:04"; got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestMODISShortName(t *testing.T) {
	if !isMODISShortName("MOD021KM", "Terra") || !isMODISShortName("MYD021KM", "Aqua") {
		t.Fatal("expected MODIS names to match their platform")
	}
	if isMODISShortName("MYD021KM", "Terra") {
		t.Fatal("Aqua product matched Terra")
	}
}
