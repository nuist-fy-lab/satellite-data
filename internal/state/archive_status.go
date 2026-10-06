package state

import (
	"context"
	"time"
)

type archiveScanRuntime struct {
	state, started, completed, lastError string
	observed                             int
}

func (s *Store) BeginArchiveScan() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanRuntime.state = "running"
	s.scanRuntime.started = time.Now().UTC().Format(time.RFC3339Nano)
	s.scanRuntime.observed = 0
	s.scanRuntime.lastError = ""
}

func (s *Store) ArchiveScanProgress(files int) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanRuntime.state == "running" {
		s.scanRuntime.observed = files
	}
}

func (s *Store) FinishArchiveScan(files int, err error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanRuntime.observed = files
	s.scanRuntime.state = "ready"
	if err != nil {
		s.scanRuntime.state = "error"
		s.scanRuntime.lastError = err.Error()
	} else {
		s.scanRuntime.completed = time.Now().UTC().Format(time.RFC3339Nano)
	}
}

type ArchiveYear struct {
	Year  int `json:"year"`
	Files int `json:"files"`
}

type ProductArchiveStatus struct {
	Product      string            `json:"product"`
	Year         int               `json:"year"`
	Scan         ArchiveScanStatus `json:"scan"`
	Files        int               `json:"files"`
	YearFiles    int               `json:"year_files"`
	FirstDay     string            `json:"first_day"`
	LastDay      string            `json:"last_day"`
	Years        []ArchiveYear     `json:"years"`
	OfficialDays int               `json:"official_days"`
	QueuedDays   int               `json:"queued_days"`
	RunningDays  int               `json:"running_days"`
	RetryDays    int               `json:"retry_days"`
}

func (s *Store) ProductArchiveIndexStatus(ctx context.Context, product string, year int) (ProductArchiveStatus, error) {
	result := ProductArchiveStatus{Product: product, Year: year, Years: []ArchiveYear{}}
	var err error
	result.Scan, err = s.ArchiveScanStatus(ctx)
	if err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT substr(slot_ts,1,4),count(DISTINCT path),
		min(substr(slot_ts,1,10)),max(substr(slot_ts,1,10)) FROM local_slots
		WHERE product_key=? GROUP BY substr(slot_ts,1,4) ORDER BY substr(slot_ts,1,4)`, product)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item ArchiveYear
		var first, last string
		if err := rows.Scan(&item.Year, &item.Files, &first, &last); err != nil {
			rows.Close()
			return result, err
		}
		result.Years = append(result.Years, item)
		result.Files += item.Files
		if result.FirstDay == "" || first < result.FirstDay {
			result.FirstDay = first
		}
		if last > result.LastDay {
			result.LastDay = last
		}
		if item.Year == year {
			result.YearFiles = item.Files
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	from, to := start.Format(time.DateOnly), start.AddDate(1, 0, 0).Format(time.DateOnly)
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_days WHERE product_key=? AND day>=? AND day<?`, product, from, to).Scan(&result.OfficialDays)
	if err != nil {
		return result, err
	}
	now := time.Now().UnixNano()
	err = s.db.QueryRowContext(ctx, `SELECT
		coalesce(sum(CASE WHEN state='pending' AND next_attempt_ns<=? THEN 1 ELSE 0 END),0),
		coalesce(sum(CASE WHEN state='running' THEN 1 ELSE 0 END),0),
		coalesce(sum(CASE WHEN state='pending' AND next_attempt_ns>? THEN 1 ELSE 0 END),0)
		FROM official_work WHERE product_key=? AND day>=? AND day<?`, now, now, product, from, to).
		Scan(&result.QueuedDays, &result.RunningDays, &result.RetryDays)
	return result, err
}
