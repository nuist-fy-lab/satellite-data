package state

import (
	"context"
	"encoding/json"
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
		for _, day := range days {
			if ctx.Err() != nil {
				return
			}
			if day.UpdatedAt == nil {
				continue
			}
			coverage, err := s.coverageDayReader(ctx, s.db, product, day.Day, false)
			if err != nil {
				return
			}
			encoded, err := json.Marshal(coverageTotals(coverage, false))
			if err != nil {
				return
			}
			// Only persist if the summary is still at the revision just read.
			// This avoids resurrecting stale colors during active downloads.
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
			if err != nil {
				return
			}
		}
	}()
}
