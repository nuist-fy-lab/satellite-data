package state

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type ArchiveDay struct {
	Product     string
	Day         string
	Fingerprint string
	Filenames   []string
}

// ReconcileGOESArchiveDays mirrors the dirty-day approach: catalogue discovery
// persists in official_work, and only changed local/catalogue revisions recount.
func (s *Store) ReconcileGOESArchiveDays(ctx context.Context, days []ArchiveDay) (queued, recounted int, err error) {
	for _, day := range days {
		if _, err := time.Parse(time.DateOnly, day.Day); err != nil {
			return queued, recounted, err
		}
		var source string
		err := s.db.QueryRowContext(ctx, `SELECT source_id FROM products WHERE product_key=?`, day.Product).Scan(&source)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return queued, recounted, err
		}
		if source != "goes16" && source != "goes17" && source != "goes18" && source != "goes19" {
			continue
		}
		n, r, err := s.reconcileGOESArchiveDay(ctx, day)
		queued += n
		recounted += r
		if err != nil {
			return queued, recounted, err
		}
	}
	return queued, recounted, nil
}

func (s *Store) reconcileGOESArchiveDay(ctx context.Context, day ArchiveDay) (int, int, error) {
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	var refreshed, officialState string
	err := s.db.QueryRowContext(ctx, `SELECT refreshed_at,state FROM official_days WHERE product_key=? AND day=?`, day.Product, day.Day).Scan(&refreshed, &officialState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	known := make(map[string]bool)
	rows, err := s.db.QueryContext(ctx, `SELECT filename FROM official_assets WHERE product_key=? AND substr(slot_ts,1,10)=?`, day.Product, day.Day)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, 0, err
		}
		known[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	missing := false
	for _, name := range day.Filenames {
		if !known[name] {
			missing = true
			break
		}
	}
	refreshedAt, _ := time.Parse(time.RFC3339Nano, refreshed)
	fingerprint := day.Fingerprint + "|" + officialState + "|" + refreshed
	var previous string
	err = s.db.QueryRowContext(ctx, `SELECT fingerprint FROM archive_day_cursor WHERE product_key=? AND day=?`, day.Product, day.Day).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	if previous == fingerprint {
		if !missing || (refreshed != "" && now.Sub(refreshedAt) < 24*time.Hour) {
			return 0, 0, nil
		}
		var pending bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM official_work WHERE product_key=? AND day=?)`, day.Product, day.Day).Scan(&pending); err != nil {
			return 0, 0, err
		}
		if pending {
			return 0, 0, nil
		}
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	// Recheck the catalogue revision after acquiring the writer connection.
	err = tx.QueryRowContext(ctx, `SELECT refreshed_at,state FROM official_days WHERE product_key=? AND day=?`, day.Product, day.Day).Scan(&refreshed, &officialState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	refreshedAt, _ = time.Parse(time.RFC3339Nano, refreshed)
	fingerprint = day.Fingerprint + "|" + officialState + "|" + refreshed
	queued := 0
	if missing && (refreshed == "" || now.Sub(refreshedAt) >= 24*time.Hour) {
		// Never reset a running lookup or its failure backoff on each scan.
		result, err := tx.ExecContext(ctx, `INSERT INTO official_work(product_key,day,state,priority,requested_at,updated_at)
			VALUES(?,?,'pending',-5,?,?) ON CONFLICT DO NOTHING`, day.Product, day.Day, stamp, stamp)
		if err != nil {
			return 0, 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		queued = int(n)
	}
	err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM archive_day_cursor WHERE product_key=? AND day=?`, day.Product, day.Day).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	recounted := 0
	if previous != fingerprint {
		if err := s.reconcileLocalSlotsTx(ctx, tx, day.Product, day.Day, stamp); err != nil {
			return 0, 0, err
		}
		if err := s.recomputeDayTx(ctx, tx, day.Product, day.Day, stamp); err != nil {
			return 0, 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO archive_day_cursor(product_key,day,fingerprint,updated_at) VALUES(?,?,?,?)
			ON CONFLICT(product_key,day) DO UPDATE SET fingerprint=excluded.fingerprint,updated_at=excluded.updated_at`,
			day.Product, day.Day, fingerprint, stamp); err != nil {
			return 0, 0, err
		}
		recounted = 1
	}
	return queued, recounted, tx.Commit()
}
