package linkmon

import "testing"

func TestJitter(t *testing.T) {
	got := jitter([]float64{100, 120, 110, 130})
	if got != 50.0/3.0 {
		t.Fatalf("jitter=%v", got)
	}
}

func TestTargetsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, target := range Targets() {
		if target.SourceID == "" || target.Host == "" || target.Port <= 0 {
			t.Fatalf("invalid target %#v", target)
		}
		if seen[target.SourceID] {
			t.Fatalf("duplicate source %s", target.SourceID)
		}
		seen[target.SourceID] = true
	}
}
