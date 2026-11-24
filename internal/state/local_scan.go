package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

type ScannedLocalFile struct {
	LocalFile
	CheckVerification bool
}

// ReplaceLocalFileScan stages batches on a dedicated connection to the read
// pool. Only the final transaction writes to the published inventory.
func (s *Store) ReplaceLocalFileScan(ctx context.Context, roots []string,
	scan func(func(ScannedLocalFile) error) ([]string, error), observe func(LocalFile)) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if s.archiveScanTempSpace != "" {
		// Discard the PostgreSQL session after the scan, including on cancellation,
		// so a session-local setting cannot be reused by another query.
		defer func() {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}()
		if _, err := conn.ExecContext(ctx,
			`SELECT set_config('temp_tablespaces', quote_ident(?), false)`, s.archiveScanTempSpace); err != nil {
			return false, err
		}
	}
	tempSchema := "pg_temp"
	if !s.postgres {
		tempSchema = "temp"
		if _, err := conn.ExecContext(ctx, "PRAGMA temp_store=FILE"); err != nil {
			return false, err
		}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanupCtx, "DROP TABLE IF EXISTS "+tempSchema+".scanned_local_files")
	}()
	skipped, err := stageLocalFileScan(ctx, conn, s.postgres, scan)
	if err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE scanned_local_files AS n SET state='ok',detail='sha256_verified:' || v.sha256
		FROM verified_archive_files v WHERE n.check_verification AND n.state<>'error'
		AND v.path=n.path AND v.size=n.size AND v.mtime_ns=n.mtime_ns`); err != nil {
		return false, err
	}
	fingerprint, err := s.inspectLocalFileScan(ctx, conn, roots, skipped, observe)
	if err != nil {
		return false, err
	}
	var prior string
	err = conn.QueryRowContext(ctx, `SELECT fingerprint FROM local_scan_state WHERE id=1`).Scan(&prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if prior == fingerprint {
		if s.archiveScanTempSpace != "" {
			return s.reconcileStaleLocalErrors(ctx, conn)
		}
		return s.ReconcileStaleLocalErrors(ctx)
	}
	return s.publishLocalFiles(ctx, conn, fingerprint, skipped)
}

func (s *Store) inspectLocalFileScan(ctx context.Context, conn *sql.Conn, roots, skipped []string, observe func(LocalFile)) (string, error) {
	order := "path"
	if s.postgres {
		order = `path COLLATE "C"`
	}
	rows, err := conn.QueryContext(ctx, `SELECT path,filename,size,mtime_ns,state,detail,source_id FROM scanned_local_files ORDER BY `+order)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for _, root := range roots {
		_, _ = hash.Write([]byte(root + "\x00"))
	}
	// Retain the fingerprint used by the in-memory scan across upgrades.
	var scratch [64]byte
	for rows.Next() {
		var file LocalFile
		if err := rows.Scan(&file.Path, &file.Filename, &file.Size, &file.MtimeNS, &file.State, &file.Detail, &file.SourceID); err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(time.Unix(0, file.MtimeNS).UTC().AppendFormat(scratch[:0], time.RFC3339Nano))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(strconv.AppendInt(scratch[:0], file.Size, 10))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strings.TrimSpace(strings.ToLower(file.Filename))))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.State + "\x00" + file.Detail + "\x00" + file.SourceID + "\x00"))
		observe(file)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, path := range skipped {
		_, _ = hash.Write([]byte(path + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func stageLocalFiles(ctx context.Context, conn *sql.Conn, files []LocalFile, postgres bool) error {
	_, err := stageLocalFileScan(ctx, conn, postgres, func(add func(ScannedLocalFile) error) ([]string, error) {
		for _, file := range files {
			if err := add(ScannedLocalFile{LocalFile: file}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return err
}

func stageLocalFileScan(ctx context.Context, conn *sql.Conn, postgres bool,
	scan func(func(ScannedLocalFile) error) ([]string, error)) ([]string, error) {
	// Stage only in the TEMP database without taking the main SQLite writer lock.
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS scanned_local_files (
		path TEXT PRIMARY KEY, filename TEXT NOT NULL, size BIGINT NOT NULL, mtime_ns BIGINT NOT NULL,
		state TEXT NOT NULL, detail TEXT NOT NULL, source_id TEXT NOT NULL, check_verification BOOLEAN NOT NULL
	)`); err != nil {
		return nil, err
	}
	begin := "BEGIN DEFERRED"
	if postgres {
		begin = "BEGIN"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return nil, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	if _, err := conn.ExecContext(ctx, "DELETE FROM scanned_local_files"); err != nil {
		return nil, err
	}
	const columns = 8
	const rowsPerBatch = 256
	const insert = "INSERT INTO scanned_local_files(path,filename,size,mtime_ns,state,detail,source_id,check_verification) VALUES "
	const row = "(?,?,?,?,?,?,?,?),"
	query := insert + strings.TrimSuffix(strings.Repeat(row, rowsPerBatch), ",")
	stmt, err := conn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	args := make([]any, 0, columns*rowsPerBatch)
	flush := func() error {
		if len(args) == 0 {
			return nil
		}
		var err error
		if len(args) == cap(args) {
			_, err = stmt.ExecContext(ctx, args...)
		} else {
			last := len(insert) + len(row)*(len(args)/columns) - 1
			_, err = conn.ExecContext(ctx, query[:last], args...)
		}
		clear(args)
		args = args[:0]
		return err
	}
	skipped, err := scan(func(file ScannedLocalFile) error {
		if file.State == "" {
			file.State = "ok"
		}
		args = append(args, file.Path, file.Filename, file.Size, file.MtimeNS, file.State, file.Detail, file.SourceID, file.CheckVerification)
		if len(args) == cap(args) {
			return flush()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return skipped, nil
}
