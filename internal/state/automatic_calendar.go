package state

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AutoCalendarMaintenance is an idempotent, background-only reconciliation
// pass. It does not require anyone to open a calendar and never resets the
// state/backoff of an existing official catalogue request.
type AutoCalendarMaintenance struct {
	TrackedYears          int
	IndexedDaysQueued     int
	CoverageDaysRecounted int
}

type calendarYear struct {
	product string
	year    int
}

// Years are inferred from all historical download orders and all previously
// indexed days. In particular, the next UTC day of a currently tracked year
// is discovered without a browser visit, even after all orders finish.
func (s *Store) autoCalendarYears(ctx context.Context, now time.Time) ([]calendarYear, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT product_key,first_year,last_year,carry_forward FROM (
		  SELECT r.collection_id product_key,substr(min(r.start_ts),1,4) first_year,
		    substr(max(r.end_ts),1,4) last_year,0 carry_forward
		    FROM download_requests r
		    JOIN products p ON p.product_key=r.collection_id
		    WHERE p.source_id<>'era5'
		    GROUP BY r.collection_id
		  UNION ALL
		  -- A year that is already substantially tracked continues to
		  -- receive new days automatically without opening the calendar.
		  -- Sparse EarthCARE/CloudSat overpasses must NOT spawn an entire
		  -- year's expensive remote searches for hundreds of products.
		  SELECT product_key,substr(day,1,4),substr(day,1,4),1
		    FROM official_days GROUP BY product_key,substr(day,1,4)
		    HAVING count(*) >= 90
		) ORDER BY product_key,first_year`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	unique := make(map[calendarYear]struct{})
	current := now.UTC().Year()
	for rows.Next() {
		var product, startYear, endYear string
		var carryForward int
		if err := rows.Scan(&product, &startYear, &endYear, &carryForward); err != nil {
			return nil, err
		}
		start, firstErr := strconv.Atoi(startYear)
		end, endErr := strconv.Atoi(endYear)
		if firstErr != nil || endErr != nil || start < 1970 || end < start {
			continue
		}
		// Clamp corrupted/accidental huge date ranges: no single order may
		// trigger millions of background catalogue requests.
		end = min(end, current, 2100)
		if start > end {
			continue
		}
		for year := start; year <= end; year++ {
			unique[calendarYear{product, year}] = struct{}{}
		}
		// A well-indexed product remains an active calendar when the UTC
		// calendar year changes. Otherwise autonomous indexing would quietly
		// stop on January 1 until somebody submits a new order.
		if carryForward == 1 && start == end && start == current-1 {
			unique[calendarYear{product, current}] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]calendarYear, 0, len(unique))
	for key := range unique {
		out = append(out, key)
	}
	// Stable order keeps metrics repeatable and preserves fairness.
	slices.SortFunc(out, func(a, b calendarYear) int {
		if c := strings.Compare(a.product, b.product); c != 0 {
			return c
		}
		return cmp.Compare(a.year, b.year)
	})
	return out, nil
}

// In contrast to EnqueueRange, this method only fills unknown days. It
// never invalidates a successful day nor resets exponential backoff.
func (s *Store) autoQueueUnknownCalendarYear(ctx context.Context, product string, year int, now time.Time, limit int) (int64, error) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	if ceiling := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1); end.After(ceiling) {
		end = ceiling
	}
	if !start.Before(end) {
		return 0, nil
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	query := `
		WITH RECURSIVE dates(day) AS (
		 SELECT ? UNION ALL SELECT date(day,'+1 day') FROM dates WHERE date(day,'+1 day') < ?
		)
		INSERT INTO official_work(product_key,day,state,priority,requested_at,updated_at)
		SELECT ?,day,'pending',-20,?,? FROM dates
		WHERE NOT EXISTS(SELECT 1 FROM official_days d WHERE d.product_key=? AND d.day=dates.day)
		  AND NOT EXISTS(SELECT 1 FROM official_work w WHERE w.product_key=? AND w.day=dates.day)
		LIMIT ? ON CONFLICT DO NOTHING`
	if s.postgres {
		query = strings.ReplaceAll(query, "date(day,'+1 day')", "((day::date + 1)::text)")
	}
	result, err := s.writer.ExecContext(ctx, query,
		start.Format(time.DateOnly), end.Format(time.DateOnly), product, stamp, stamp, product, product, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Recount missing or stale derived summaries after an interrupted archive
// scan or migration. Ordinary live catalogue saves and changed local
// inventory already do exact per-day recounts; this repairs old omissions.
func (s *Store) AutoRecountStaleCalendar(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 24
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.product_key,d.day FROM official_days d
		LEFT JOIN day_summary summary ON summary.product_key=d.product_key AND summary.day=d.day
		WHERE summary.product_key IS NULL OR summary.updated_at<d.refreshed_at
		OR EXISTS(SELECT 1 FROM local_slots l WHERE l.product_key=d.product_key
		  AND substr(l.slot_ts,1,10)=d.day AND l.updated_at>summary.updated_at)
		ORDER BY d.refreshed_at DESC,d.product_key,d.day LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var stale []calendarDay
	for rows.Next() {
		var item calendarDay
		if err := rows.Scan(&item.product, &item.day); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(stale) == 0 {
		return 0, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range stale {
		if err := s.reconcileLocalSlotsTx(ctx, tx, item.product, item.day, stamp); err != nil {
			return 0, err
		}
		if err := s.recomputeDayTx(ctx, tx, item.product, item.day, stamp); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

type calendarDay struct{ product, day string }

func (s *Store) AutoMaintainCalendar(ctx context.Context, now time.Time) (AutoCalendarMaintenance, error) {
	var result AutoCalendarMaintenance
	targets, err := s.autoCalendarYears(ctx, now)
	if err != nil {
		return result, fmt.Errorf("find tracked calendar years: %w", err)
	}
	result.TrackedYears = len(targets)
	// Do not enqueue enormous backlogs on shared public catalogues when the
	// user has thousands of products. This global budget is deliberately
	// below the explicit order/index priority and advances every sweep.
	const maxNewDaysPerSweep = 450
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// All requests are low priority and deduplicated. No blocking
		// download workers and no repeated upstream requests for known days.
		remaining := maxNewDaysPerSweep - result.IndexedDaysQueued
		if remaining <= 0 {
			break
		}
		queued, err := s.autoQueueUnknownCalendarYear(ctx, target.product, target.year, now, min(remaining, 150))
		if err != nil {
			return result, fmt.Errorf("auto index %s %d: %w", target.product, target.year, err)
		}
		result.IndexedDaysQueued += int(queued)
	}
	count, err := s.AutoRecountStaleCalendar(ctx, 24)
	result.CoverageDaysRecounted = count
	if err != nil && !errors.Is(err, context.Canceled) {
		return result, fmt.Errorf("recalculate calendar coverage: %w", err)
	}
	return result, err
}
