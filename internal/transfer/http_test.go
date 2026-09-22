package transfer

import (
	"testing"
	"time"
)

func TestParseContentRange(t *testing.T) {
	start, end, total, ok := parseContentRange("bytes 100-199/1000")
	if !ok || start != 100 || end != 199 || total != 1000 {
		t.Fatalf("unexpected parse: %d %d %d %v", start, end, total, ok)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := parseRetryAfter("7"); got != 7*time.Second {
		t.Fatalf("retry-after=%s", got)
	}
}

func TestSafeName(t *testing.T) {
	if _, err := safeName("file.nc"); err != nil {
		t.Fatalf("safe name rejected: %v", err)
	}
	for _, name := range []string{"../file.nc", "dir/file.nc", "", "."} {
		if _, err := safeName(name); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
}
