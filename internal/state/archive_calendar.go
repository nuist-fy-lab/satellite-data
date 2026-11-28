package state

import (
	"context"
	"errors"
	"strings"
	"time"
)

type ArchiveCalendarProduct struct {
	Product string `json:"product"`
	// Each span is [first UTC day of year, exclusive end, status]. Ordinals
	// start at zero. Statuses are partial (0), complete (1), complete with
	// official gaps (2), hourly complete (3), hourly with gaps (4).
	Spans [][3]int `json:"spans"`
}

// VisitArchiveCalendar visits only products and UTC days with at least one
// valid local file. It jumps to the next day using the slot index, so a dense
// archive does not require reading all its files or scheduling official work.
func (s *Store) VisitArchiveCalendar(ctx context.Context, sources []string, year int, visit func(ArchiveCalendarProduct) error) error {
	if year < 1970 || year > 2100 || len(sources) == 0 {
		return errors.New("invalid archive calendar selection")
	}
	end := time.Date(year+1, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	args := make([]any, 0, len(sources)+3)
	args = append(args, start, end)
	for _, source := range sources {
		args = append(args, source)
	}
	args = append(args, end)
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE archived(product_key, slot_ts) AS (
			SELECT p.product_key, (SELECT l.slot_ts FROM local_slots l
				WHERE l.product_key=p.product_key AND l.state='ok' AND l.slot_ts>=? AND l.slot_ts<?
				ORDER BY l.slot_ts LIMIT 1)
			FROM products p WHERE p.source_id IN (`+strings.Repeat("?,", len(sources)-1)+`?)
			UNION ALL
			SELECT a.product_key, (SELECT l.slot_ts FROM local_slots l
				WHERE l.product_key=a.product_key AND l.state='ok'
				  AND l.slot_ts>substr(a.slot_ts,1,10)||'Z' AND l.slot_ts<?
				ORDER BY l.slot_ts LIMIT 1)
			FROM archived a WHERE a.slot_ts IS NOT NULL
		)
		SELECT a.product_key,substr(a.slot_ts,1,10),COALESCE(d.status,'partial')
		FROM archived a LEFT JOIN day_summary d ON d.product_key=a.product_key AND d.day=substr(a.slot_ts,1,10)
		WHERE a.slot_ts IS NOT NULL ORDER BY a.product_key,a.slot_ts`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var current ArchiveCalendarProduct
	for rows.Next() {
		var product, day, status string
		if err := rows.Scan(&product, &day, &status); err != nil {
			return err
		}
		at, err := time.Parse(time.DateOnly, day)
		if err != nil {
			return err
		}
		if current.Product != product {
			if len(current.Spans) > 0 {
				if err := visit(current); err != nil {
					return err
				}
			}
			current = ArchiveCalendarProduct{Product: product}
		}
		ordinal, code := at.YearDay()-1, archiveCalendarStatus(status)
		if count := len(current.Spans); count > 0 {
			last := &current.Spans[count-1]
			if last[1] == ordinal && last[2] == code {
				last[1]++
				continue
			}
		}
		current.Spans = append(current.Spans, [3]int{ordinal, ordinal + 1, code})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(current.Spans) > 0 {
		return visit(current)
	}
	return nil
}

func archiveCalendarStatus(status string) int {
	switch status {
	case "complete":
		return 1
	case "complete_official_gap":
		return 2
	case "hourly_complete":
		return 3
	case "hourly_complete_official_gap":
		return 4
	default:
		return 0
	}
}
