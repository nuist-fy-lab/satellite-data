package state

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
)

// calendarDownloadIntent describes active download requests, not index jobs.
// A selected UTC time is marked as processing only while its file is absent.
type calendarDownloadIntent struct {
	allBands map[int]struct{}
	times    map[string]map[int]struct{}
}

func (i *calendarDownloadIntent) matches(at time.Time, band int) bool {
	if i == nil {
		return false
	}
	if _, ok := i.allBands[0]; ok {
		return true
	}
	if _, ok := i.allBands[band]; ok {
		return true
	}
	selected := i.times[at.UTC().Format("15:04")]
	if _, ok := selected[0]; ok {
		return true
	}
	_, ok := selected[band]
	return ok
}

func (i *calendarDownloadIntent) count() int {
	if i == nil {
		return 0
	}
	return len(i.times)
}

func (i *calendarDownloadIntent) matchesAny(at time.Time) bool {
	if i == nil {
		return false
	}
	return len(i.allBands) > 0 || len(i.times[at.UTC().Format("15:04")]) > 0
}

// Active orders are separate from official indexing and local file scanning.
// Keep this as a read-only overlay so an order cannot turn unselected slots
// yellow, and a completed/failed/cancelled order cannot remain processing.
func (s *Store) activeCalendarDownloads(ctx context.Context, product string, start, end time.Time) (map[string]*calendarDownloadIntent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start_ts,end_ts,selection_json FROM download_requests
		WHERE collection_id=? AND state IN ('pending','discovering','retry_wait','transferring','waiting_auth')
		AND start_ts<? AND end_ts>?`, product, formatTime(end), formatTime(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]*calendarDownloadIntent)
	for rows.Next() {
		var first, last, encoded string
		if err := rows.Scan(&first, &last, &encoded); err != nil {
			return nil, err
		}
		from, err := time.Parse(time.RFC3339Nano, first)
		if err != nil {
			return nil, err
		}
		until, err := time.Parse(time.RFC3339Nano, last)
		if err != nil {
			return nil, err
		}
		var selection downloadfilter.Selection
		if err := json.Unmarshal([]byte(encoded), &selection); err != nil {
			return nil, err
		}
		if from.Before(start) {
			from = start
		}
		if until.After(end) {
			until = end
		}
		for day := dayFloor(from); day.Before(until); day = day.AddDate(0, 0, 1) {
			key := day.Format(time.DateOnly)
			intent := result[key]
			if intent == nil {
				intent = &calendarDownloadIntent{allBands: make(map[int]struct{}), times: make(map[string]map[int]struct{})}
				result[key] = intent
			}
			if len(selection.Times) == 0 {
				intent.allBands[selection.Band] = struct{}{}
				continue
			}
			for _, clock := range selection.Times {
				at, parseErr := time.Parse(time.RFC3339, key+"T"+clock+":00Z")
				if parseErr == nil && !at.Before(from) && at.Before(until) {
					if intent.times[clock] == nil {
						intent.times[clock] = make(map[int]struct{})
					}
					intent.times[clock][selection.Band] = struct{}{}
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) calendarIntentForDay(ctx context.Context, product, day string) (*calendarDownloadIntent, error) {
	at, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil, err
	}
	items, err := s.activeCalendarDownloads(ctx, product, at, at.AddDate(0, 0, 1))
	return items[day], err
}
