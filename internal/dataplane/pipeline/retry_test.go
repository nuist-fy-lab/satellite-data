package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/config"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/model"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/store"
	"github.com/nuist-fy-lab/satellite-data/internal/dataplane/transfer"
)

func TestExponentialRetryEqualJitterIsBoundedAndIncreasing(t *testing.T) {
	var previous time.Duration
	for _, tc := range []struct {
		attempt int
		base    time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 80 * time.Second},
		{6, 160 * time.Second},
		{7, 320 * time.Second},
		{8, 10 * time.Minute},
		{1000, 10 * time.Minute},
	} {
		low := retryDelayWithJitter(tc.attempt, func(int64) int64 { return 0 })
		high := retryDelayWithJitter(tc.attempt, func(bound int64) int64 { return bound - 1 })
		if low != tc.base/2 || high != tc.base {
			t.Errorf("attempt=%d jitter range [%v,%v], want [%v,%v]", tc.attempt, low, high, tc.base/2, tc.base)
		}
		if low < previous {
			t.Errorf("attempt=%d did not back off: previous=%v current=%v", tc.attempt, previous, low)
		}
		previous = low
	}
	if got := retryDelayWithJitter(0, func(int64) int64 { return 0 }); got != retryBaseDelay/2 {
		t.Fatalf("zero attempts generated invalid delay: %v", got)
	}
	if got := retryDelay(1000000); got < retryMaxDelay/2 || got > retryMaxDelay {
		t.Fatalf("extreme attempt must be capped: %v", got)
	}
}

func TestRetryPipelineCountsOnly404AndFailsAfterWindow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	item, _, err := s.Enqueue(ctx, model.TaskSpec{Key: "mixed", SourceID: "modis", Name: "file", URL: "https://example.invalid/file"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE tasks SET attempts=100 WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.ClaimTransfer(ctx, "modis")
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	runtime := &Runtime{store: s}
	runtime.scheduleTransferRetry(ctx, "modis", task, &transfer.HTTPError{Status: http.StatusNotFound})
	pending, err := s.Task(ctx, item.ID)
	if err != nil || pending.State != "retry_wait" || pending.Missing404Count != 1 {
		t.Fatalf("first 404 after mixed attempts: %+v %v", pending, err)
	}
	if _, err := db.Exec(`UPDATE tasks SET next_attempt_ns=0,missing_404_count=5,first_404_at=? WHERE id=?`, time.Now().Add(-7*time.Hour).UTC().Format(time.RFC3339Nano), item.ID); err != nil {
		t.Fatal(err)
	}
	task, ok, err = s.ClaimTransfer(ctx, "modis")
	if err != nil || !ok {
		t.Fatalf("second claim: %v %v", ok, err)
	}
	runtime.scheduleTransferRetry(ctx, "modis", task, &transfer.HTTPError{Status: http.StatusNotFound})
	pending, err = s.Task(ctx, item.ID)
	if err != nil || pending.State != "failed" || pending.Missing404Count != 6 {
		t.Fatalf("confirmed 404 not terminal: %+v %v", pending, err)
	}
}

func TestRetryPolicyForEverySupportedProduct(t *testing.T) {
	for _, provider := range config.Load().Providers {
		t.Run(provider.SourceID, func(t *testing.T) {
			now := time.Now()
			missing := &transfer.HTTPError{Status: http.StatusNotFound}
			for _, task := range []model.Task{
				{Attempts: 100},
				{Attempts: 100, Missing404Count: 4, First404At: now.Add(-7 * time.Hour)},
				{Attempts: 100, Missing404Count: 20, First404At: now.Add(-time.Hour)},
			} {
				if got := transferRetryDecision(task, missing, now); got.terminal || got.delay <= 0 {
					t.Fatalf("premature 404 failure: %+v", got)
				}
			}
			if got := transferRetryDecision(model.Task{Attempts: 6, Missing404Count: 5, First404At: now.Add(-6 * time.Hour)}, missing, now); !got.terminal {
				t.Fatalf("confirmed 404 should fail: %+v", got)
			}
			if got := transferRetryDecision(model.Task{Attempts: 1}, &transfer.HTTPError{Status: http.StatusGone}, now); !got.terminal {
				t.Fatalf("410 should fail: %+v", got)
			}
			for _, status := range []int{http.StatusRequestTimeout, http.StatusTooEarly,
				http.StatusTooManyRequests, http.StatusInternalServerError,
				http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
				if got := transferRetryDecision(model.Task{Attempts: 24}, &transfer.HTTPError{Status: status}, now); got.terminal || got.delay < retryMaxDelay/2 {
					t.Fatalf("HTTP %d should keep retrying through outages: %+v", status, got)
				}
			}
			if got := transferRetryDecision(model.Task{Attempts: 1}, &net.DNSError{IsTimeout: true}, now); got.terminal || got.delay <= 0 {
				t.Fatalf("network timeout should retry: %+v", got)
			}
			if got := transferRetryDecision(model.Task{Attempts: 1}, errors.New("temporary central gateway failure"), now); got.terminal || got.delay <= 0 {
				t.Fatalf("central service outage should retry: %+v", got)
			}
			for _, status := range []int{http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity} {
				if got := transferRetryDecision(model.Task{Attempts: 1}, &transfer.HTTPError{Status: status}, now); !got.terminal {
					t.Fatalf("malformed HTTP %d request must not retry forever: %+v", status, got)
				}
			}
		})
	}
}

func TestRetryAfterIsMinimumAndRetainsIndependentJitter(t *testing.T) {
	for _, delay := range []time.Duration{time.Minute, time.Hour} {
		var first, last time.Duration
		for i := 0; i < 40; i++ {
			got := transferRetryDecision(model.Task{Attempts: 1}, &transfer.HTTPError{
				Status: http.StatusTooManyRequests, RetryAfter: delay,
			}, time.Now())
			if got.terminal || got.delay < delay || got.delay > delay+min(delay/4, 5*time.Minute) {
				t.Fatalf("Retry-After %s was ignored: %+v", delay, got)
			}
			if i == 0 || got.delay < first {
				first = got.delay
			}
			if got.delay > last {
				last = got.delay
			}
		}
		if first == last {
			t.Fatalf("Retry-After %s had no jitter", delay)
		}
	}
}

func TestRetryPolicyPersistsFutureScheduleAndTerminal404(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "download.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	runtime := &Runtime{store: state}
	for _, source := range []string{"modis", "goes", "earthcare", "era5"} {
		t.Run(source, func(t *testing.T) {
			_, _, err := state.Enqueue(ctx, model.TaskSpec{
				Key: fmt.Sprintf("%s-retry", source), SourceID: source,
				CollectionID: source + ":collection", GranuleID: "granule",
				AssetID: "asset", Name: "data.nc",
				URL: "https://example.invalid/data.nc", RelativeDir: source + "/2026",
			})
			if err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := state.ClaimTransfer(ctx, source)
			if err != nil || !ok || claimed.Attempts != 1 {
				t.Fatalf("claim failed: %+v ok=%t err=%v", claimed, ok, err)
			}
			before := time.Now()
			runtime.scheduleTransferRetry(ctx, source, claimed, &transfer.HTTPError{Status: http.StatusNotFound})
			pending, err := state.Task(ctx, claimed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pending.State != "retry_wait" || pending.NextAttemptAt.Before(before.Add(2*time.Second)) ||
				pending.NextAttemptAt.After(before.Add(6*time.Second)) {
				t.Fatalf("404 retry was not scheduled with persisted delay: %+v", pending)
			}
			// This must not touch other products' queued tasks or produce a
			// request storm when the upstream object is temporarily absent.
			if _, ok, err := state.ClaimTransfer(ctx, source); err != nil || ok {
				t.Fatalf("backoff bypassed: ok=%t err=%v", ok, err)
			}
			if pending.Missing404Count != 1 || pending.First404At.IsZero() {
				t.Fatalf("404 evidence not persisted: %+v", pending)
			}
		})
	}
}
