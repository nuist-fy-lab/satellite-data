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

func TestMTGCatalogClassification(t *testing.T) {
	if !isMTGCollection("FCI Level 1c Normal Resolution Image Data - MTG - 0 degree") {
		t.Fatal("MTG collection was not recognized")
	}
	if got := mtgInstrument("LI Lightning Flashes - MTG - 0 degree"); got != "LI" {
		t.Fatalf("instrument=%q", got)
	}
	if got := mtgLevel("FCI Level 1c High Resolution Image Data - MTG - 0 degree"); got != "L1c" {
		t.Fatalf("level=%q", got)
	}
	if got := mtgArea("Cloud Mask - MTG - 0 degree"); got != "DISK" {
		t.Fatalf("area=%q", got)
	}
}
