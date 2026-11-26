package egress

import (
	"context"
	"testing"
	"time"
)

func TestFastRouteExtraSlotRequiresUsefulThroughputGain(t *testing.T) {
	for _, test := range []struct {
		name  string
		bytes int
		want  string
	}{
		{"keeps useful slot", 45_000_000, "confirmed"},
		{"rolls back small gain", 31_000_000, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, now := newTimedPool(&Config{
				Routes:      []Route{{ID: "fast", PublicIP: "192.0.2.1"}},
				PerIPLimits: map[string]int{"modis": 1}, ExploreFastRouteCapacity: true,
			}, time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC))
			take := func() func() {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
				defer cancel()
				_, release, err := p.Acquire(ctx, "modis")
				if err != nil {
					t.Fatal(err)
				}
				return release
			}
			defer take()()
			*now = now.Add(capacityWindow)
			p.RecordSourceBytes("modis", "fast", 30_000_000)
			extra := take()
			defer extra()
			if got := p.Snapshot()[0].ExtraBySource["modis"]; got != "trial" {
				t.Fatalf("expected one extra trial, got %q", got)
			}
			*now = now.Add(capacityWindow)
			p.RecordSourceBytes("modis", "fast", test.bytes)
			status := p.Snapshot()[0]
			if got := status.ExtraBySource["modis"]; got != test.want || status.ActiveTransfers != 2 {
				t.Fatalf("wrong capacity state after trial: %+v", status)
			}
			extra()
			if test.want != "" {
				take()()
				return
			}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			if _, release, err := p.Acquire(ctx, "modis"); err == nil {
				release()
				t.Fatal("unsuccessful trial still admitted extra transfers")
			}
		})
	}
}
