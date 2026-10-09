package state

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// queueCoverageWarmup upgrades legacy days without delaying calendar HTTP
// requests. Only one cold-archive reader runs per Store; other years queue.
// A recomputed day's durable revision always wins over any stale backfill.
func (s *Store) queueCoverageWarmup(product model.Product, year int, days []YearDay) {
	key := product.ProductKey + "|" + time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006")
	s.coverageWarmupMu.Lock()
	if s.coverageWarmups[key] || s.coverageWarmupSlots == nil {
		s.coverageWarmupMu.Unlock()
		return
	}
	if s.coverageWarmups == nil {
		s.coverageWarmups = make(map[string]bool)
	}
	s.coverageWarmups[key] = true
	s.coverageWarmupMu.Unlock()
	go func() {
		defer func() {
			s.coverageWarmupMu.Lock()
			delete(s.coverageWarmups, key)
			s.coverageWarmupMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(s.coverageWarmupCtx, 15*time.Minute)
		defer cancel()
		select {
		case s.coverageWarmupSlots <- struct{}{}:
			defer func() { <-s.coverageWarmupSlots }()
		case <-ctx.Done():
			return
		}
		remaining := days
		for attempt := 0; attempt < 6 && len(remaining) > 0; attempt++ {
			for i, day := range remaining {
				if ctx.Err() != nil {
					return
				}
				if err := s.warmCoverageDay(ctx, product, day); err != nil {
					// The background importer competes with active catalogue/index
					// writers. Do not abandon the remaining year on a transient
					// SQLite busy timeout; keep already materialized days.
					log.Printf("WARN coverage year warmup retry product=%s day=%s attempt=%d error=%v",
						product.ProductKey, day.Day, attempt+1, err)
					remaining = remaining[i:]
					break
				}
				if i == len(remaining)-1 {
					remaining = nil
				}
			}
			if len(remaining) == 0 {
				break
			}
			backoff := time.Duration(1<<attempt) * time.Second
			wait := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				wait.Stop()
				return
			case <-wait.C:
			}
		}
		if len(remaining) > 0 {
			log.Printf("WARN coverage year warmup deferred product=%s year=%d remaining=%d",
				product.ProductKey, year, len(remaining))
		}
	}()
}

func (s *Store) warmCoverageDay(ctx context.Context, product model.Product, day YearDay) error {
	if day.UpdatedAt == nil {
		return nil
	}
	coverage, err := s.coverageDayReader(ctx, s.db, product, day.Day, false)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(coverageTotals(coverage, false))
	if err != nil {
		return err
	}
	// The versioned update condition prevents stale background work from
	// overwriting a newer day computed by the official indexing worker.
	_, err = s.writer.ExecContext(ctx, `
				INSERT INTO coverage_day_cache(product_key,day,revision,totals_json,official_state,timeline_mode)
				SELECT ?,?,?,?,?,? FROM day_summary
				WHERE product_key=? AND day=? AND updated_at=?
				ON CONFLICT(product_key,day) DO UPDATE SET revision=excluded.revision,
				 totals_json=excluded.totals_json,official_state=excluded.official_state,
				 timeline_mode=excluded.timeline_mode`,
		product.ProductKey, day.Day, coverageCacheVersion+product.Cadence+"|"+*day.UpdatedAt,
		string(encoded), coverage.OfficialState, coverage.Mode,
		product.ProductKey, day.Day, *day.UpdatedAt)
	return err
}
