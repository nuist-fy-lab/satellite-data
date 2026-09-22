package eumetsat

import (
	"testing"
	"time"
)

func TestParseDateProperty(t *testing.T) {
	start, end, ok := parseDateProperty("2026-09-21T00:00:07.000Z/2026-09-21T00:09:24.000Z")
	if !ok {
		t.Fatal("date property did not parse")
	}
	if want := time.Date(2026, 9, 21, 0, 0, 7, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start=%s want=%s", start, want)
	}
	if want := time.Date(2026, 9, 21, 0, 9, 24, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end=%s want=%s", end, want)
	}
}

func TestKnownMTGCollections(t *testing.T) {
	for _, id := range []string{"EO:EUM:DAT:0662", "EO:EUM:DAT:0665"} {
		if !knownCollection(id) {
			t.Fatalf("expected %s to be supported", id)
		}
	}
}
