package state

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
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
	for _, selected := range i.bandsAt(at) {
		if _, ok := selected[0]; ok {
			return true
		}
		if _, ok := selected[band]; ok {
			return true
		}
	}
	return false
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
	if len(i.allBands) > 0 {
		return true
	}
	for _, bands := range i.bandsAt(at) {
		if len(bands) > 0 {
			return true
		}
	}
	return false
}

func (i *calendarDownloadIntent) bandsAt(at time.Time) []map[int]struct{} {
	at = at.UTC()
	bands := []map[int]struct{}{i.times[at.Format("15:04")]}
	if at.Second() != 0 && at.Nanosecond() == 0 {
		bands = append(bands, i.times[at.Format("15:04:05")])
	}
	return bands
}

// Stable request selection signature for the coverage cache. Rebuilding a
// multi-year index every SSE tick would otherwise repeat expensive official
// and local-file joins for every active day.
func (i *calendarDownloadIntent) revision() string {
	if i == nil {
		return ""
	}
	var b strings.Builder
	appendBands := func(bands map[int]struct{}) {
		values := make([]int, 0, len(bands))
		for band := range bands {
			values = append(values, band)
		}
		sort.Ints(values)
		for _, band := range values {
			b.WriteString(strconv.Itoa(band))
			b.WriteByte(',')
		}
	}
	appendBands(i.allBands)
	clocks := make([]string, 0, len(i.times))
	for clock := range i.times {
		clocks = append(clocks, clock)
	}
	sort.Strings(clocks)
	for _, clock := range clocks {
		b.WriteByte('|')
		b.WriteString(clock)
		b.WriteByte('=')
		appendBands(i.times[clock])
	}
	return b.String()
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
		// Granule IDs have no reliable time mapping in the request record.
		// Actual file states are overlaid separately by the calendar.
		if len(selection.GranuleIDs) > 0 {
			continue
		}
		if from.Before(start) {
			from = start
		}
		if until.After(end) {
			until = end
		}
		for day := dayFloor(from); day.Before(until); day = day.AddDate(0, 0, 1) {
			key := day.Format(time.DateOnly)
			clocks := selection.Times
			if len(selection.Scenes) > 0 {
				clocks = selection.Scenes[key]
				if len(clocks) == 0 {
					continue
				}
			}
			intent := result[key]
			if intent == nil {
				intent = &calendarDownloadIntent{allBands: make(map[int]struct{}), times: make(map[string]map[int]struct{})}
				result[key] = intent
			}
			if len(clocks) == 0 {
				intent.allBands[selection.Band] = struct{}{}
				continue
			}
			for _, clock := range clocks {
				clockTime, parseErr := time.Parse("15:04", clock)
				if parseErr != nil {
					clockTime, parseErr = time.Parse("15:04:05", clock)
				}
				if parseErr != nil {
					continue
				}
				at := day.Add(time.Duration(clockTime.Hour())*time.Hour + time.Duration(clockTime.Minute())*time.Minute + time.Duration(clockTime.Second())*time.Second)
				if !at.Before(from) && at.Before(until) {
					if clockTime.Second() == 0 {
						clock = clockTime.Format("15:04")
					}
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
