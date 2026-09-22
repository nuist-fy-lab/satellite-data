package himawari

import (
	"testing"
	"time"
)

func TestParseCycleFromObject(t *testing.T) {
	name := "AHI-L1b-FLDK/2026/09/22/0500/HS_H09_20260922_0500_B03_FLDK_R05_S0110.DAT.bz2"
	got, ok := parseCycleFromObject("AHI-L1b-FLDK", name)
	if !ok {
		t.Fatal("expected object path to parse")
	}
	want := time.Date(2026, 9, 22, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("cycle=%s want=%s", got, want)
	}
}

func TestHimawariCadence(t *testing.T) {
	if got := himawariCadence("AHI-L1b-FLDK"); got != "10m" {
		t.Fatalf("cadence=%q", got)
	}
	if got := himawariCadence("AHI-L1b-Target"); got != "event" {
		t.Fatalf("cadence=%q", got)
	}
}
