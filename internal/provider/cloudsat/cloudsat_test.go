package cloudsat

import (
	"testing"
	"time"
)

func TestParseFilenameTime(t *testing.T) {
	got, ok := parseFilenameTime("2017033223812_57288_CS_2C-ICE_GRANULE_P1_R05_E06_F01.hdf")
	if !ok {
		t.Fatal("expected CloudSat timestamp to parse")
	}
	want := time.Date(2017, time.February, 2, 22, 38, 12, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parseFilenameTime() = %s, want %s", got, want)
	}
}
