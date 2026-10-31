package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

const regionalOfficialSchema = `CREATE TABLE IF NOT EXISTS regional_official_progress (
 product_key TEXT NOT NULL, day TEXT NOT NULL, next_window INTEGER NOT NULL,
 granules_json TEXT NOT NULL, PRIMARY KEY(product_key, day))`

func IsRegionalOfficialProduct(product string) bool {
	switch product {
	case "himawari8:AHI-L1b-Japan", "himawari8:AHI-L1b-Target", "himawari9:AHI-L1b-Japan", "himawari9:AHI-L1b-Target":
		return true
	default:
		return false
	}
}

func regionalDay(batch WorkBatch) (time.Time, error) {
	if !IsRegionalOfficialProduct(batch.Product) || len(batch.Days) != 1 || batch.ClaimedAt == "" {
		return time.Time{}, errors.New("invalid regional claim")
	}
	return time.Parse(time.DateOnly, batch.Days[0])
}

func (s *Store) RegionalOfficialWindow(ctx context.Context, batch WorkBatch) (time.Time, time.Time, error) {
	day, err := regionalDay(batch)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	var state, claimed string
	if err := s.db.QueryRowContext(ctx, `SELECT state,started_at FROM official_work WHERE product_key=? AND day=?`, batch.Product, batch.Days[0]).Scan(&state, &claimed); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if state != "running" || claimed != batch.ClaimedAt {
		return time.Time{}, time.Time{}, errors.New("regional claim is no longer active")
	}
	var next int
	err = s.db.QueryRowContext(ctx, `SELECT next_window FROM regional_official_progress WHERE product_key=? AND day=?`, batch.Product, batch.Days[0]).Scan(&next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, time.Time{}, err
	}
	if next < 0 || next >= 144 {
		return time.Time{}, time.Time{}, errors.New("invalid regional progress")
	}
	start := day.Add(time.Duration(next) * 10 * time.Minute)
	return start, start.Add(10 * time.Minute), nil
}

func (s *Store) SaveRegionalOfficialWindow(ctx context.Context, batch WorkBatch, start time.Time, granules []model.Granule) (bool, error) {
	day, err := regionalDay(batch)
	if err != nil {
		return false, err
	}
	offset := start.Sub(day)
	if offset < 0 || offset >= 24*time.Hour || offset%(10*time.Minute) != 0 {
		return false, errors.New("invalid regional window")
	}
	want := int(offset / (10 * time.Minute))
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	query := `SELECT state,started_at FROM official_work WHERE product_key=? AND day=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var state, claimed string
	if err := tx.QueryRowContext(ctx, query, batch.Product, batch.Days[0]).Scan(&state, &claimed); err != nil {
		return false, err
	}
	if state != "running" || claimed != batch.ClaimedAt {
		return false, errors.New("regional claim is no longer active")
	}
	var next int
	var encoded string
	err = tx.QueryRowContext(ctx, `SELECT next_window,granules_json FROM regional_official_progress WHERE product_key=? AND day=?`, batch.Product, batch.Days[0]).Scan(&next, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		encoded = "[]"
	} else if err != nil {
		return false, err
	}
	if next != want {
		return false, errors.New("regional window no longer current")
	}
	var previous []model.Granule
	if err := json.Unmarshal([]byte(encoded), &previous); err != nil {
		return false, err
	}
	merged := make(map[string]model.Granule, len(previous)+len(granules))
	for _, g := range append(previous, granules...) {
		if g.ID == "" || g.Start.IsZero() {
			return false, errors.New("regional granule missing identity or time")
		}
		// A successful provider search returns the complete current group.
		merged[g.ID] = g
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	all := make([]model.Granule, 0, len(keys))
	for _, key := range keys {
		all = append(all, merged[key])
	}
	if want == 143 {
		if err := s.saveOfficialBatchTx(ctx, tx, batch, all); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM regional_official_progress WHERE product_key=? AND day=?`, batch.Product, batch.Days[0]); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	data, err := json.Marshal(all)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO regional_official_progress(product_key,day,next_window,granules_json) VALUES(?,?,?,?) ON CONFLICT(product_key,day) DO UPDATE SET next_window=excluded.next_window,granules_json=excluded.granules_json`, batch.Product, batch.Days[0], want+1, string(data)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE official_work SET state='pending',failure_count=0,next_attempt_ns=0,last_error='',updated_at=? WHERE product_key=? AND day=?`, time.Now().UTC().Format(time.RFC3339Nano), batch.Product, batch.Days[0]); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func (s *Store) markRegionalBatchError(ctx context.Context, batch WorkBatch, day string, cause error) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `SELECT failure_count FROM official_work WHERE product_key=? AND day=? AND state='running' AND started_at=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var failures int
	if err := tx.QueryRowContext(ctx, query, batch.Product, day, batch.ClaimedAt).Scan(&failures); err != nil {
		return fmt.Errorf("regional claim error: %w", err)
	}
	now := time.Now()
	failures++
	if _, err := tx.ExecContext(ctx, `UPDATE official_work SET state='pending',failure_count=?,next_attempt_ns=?,last_error=?,updated_at=? WHERE product_key=? AND day=?`, failures, now.Add(officialRetryDelay(failures)).UnixNano(), cause.Error(), now.UTC().Format(time.RFC3339Nano), batch.Product, day); err != nil {
		return err
	}
	return tx.Commit()
}
