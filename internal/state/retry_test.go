package state

import (
	"testing"
	"time"
)

func TestOfficialRetryDelayJitterIsBounded(t *testing.T) {
	for _, tc := range []struct {
		failures int
		min      time.Duration
		max      time.Duration
	}{{1, time.Minute, 90 * time.Second}, {2, 2 * time.Minute, 3 * time.Minute},
		{8, time.Hour, time.Hour}} {
		for range 16 {
			delay := officialRetryDelay(tc.failures)
			if delay < tc.min || delay > tc.max {
				t.Fatalf("failure %d: delay %s outside [%s, %s]", tc.failures, delay, tc.min, tc.max)
			}
		}
	}
}
