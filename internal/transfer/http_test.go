package transfer

import "testing"

func TestSafeRelativeDir(t *testing.T) {
	for _, value := range []string{"goes/2026/09/24", "modis/Terra"} {
		if _, err := safeRelativeDir(value); err != nil {
			t.Fatalf("safe path %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "..", "../escape", "/absolute"} {
		if _, err := safeRelativeDir(value); err == nil {
			t.Fatalf("unsafe path %q accepted", value)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	start, end, total, ok := parseContentRange("bytes 100-199/1000")
	if !ok || start != 100 || end != 199 || total != 1000 {
		t.Fatalf("unexpected range %d-%d/%d ok=%v", start, end, total, ok)
	}
}
