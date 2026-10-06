package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
)

const era5OrderSchema = `
CREATE TABLE IF NOT EXISTS era5_order_inputs(
 request_id INTEGER PRIMARY KEY REFERENCES download_requests(id) ON DELETE CASCADE,
 inputs_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS era5_order_parts(
 request_id INTEGER PRIMARY KEY REFERENCES download_requests(id) ON DELETE CASCADE,
 start_ts TEXT NOT NULL,end_ts TEXT NOT NULL,inputs_json TEXT NOT NULL,
 stage TEXT NOT NULL DEFAULT 'pending',job_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS era5_result_manifest(
 downloader_task_id INTEGER PRIMARY KEY,
 request_id INTEGER NOT NULL REFERENCES download_requests(id) ON DELETE CASCADE,
 job_id TEXT NOT NULL,
 collection_id TEXT NOT NULL,
 asset_id TEXT NOT NULL,
 filename TEXT NOT NULL,
 size INTEGER NOT NULL,
 recorded_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS era5_result_identity_idx ON era5_result_manifest(collection_id,job_id,asset_id,size);
CREATE TABLE IF NOT EXISTS era5_result_slices(
 downloader_task_id INTEGER PRIMARY KEY REFERENCES era5_result_manifest(downloader_task_id) ON DELETE CASCADE,
 start_ts TEXT NOT NULL,end_ts TEXT NOT NULL,inputs_json TEXT NOT NULL
);`

type ERA5Part struct {
	Start, End   time.Time
	Submission   era5.Submission
	Stage, JobID string
}

func (s *Store) RecordERA5Result(ctx context.Context, requestID, taskID int64, jobID, collectionID, assetID, filename string, size int64, slices ...ERA5Part) error {
	if requestID <= 0 || taskID <= 0 || jobID == "" || collectionID == "" || assetID == "" || filename == "" || size <= 0 {
		return errors.New("invalid ERA5 result provenance")
	}
	if len(slices) > 1 {
		return errors.New("multiple ERA5 result slices")
	}
	if len(slices) == 1 {
		part := slices[0]
		if !part.End.After(part.Start) || part.Submission.CollectionID != collectionID || (part.JobID != "" && part.JobID != jobID) {
			return errors.New("invalid ERA5 result slice")
		}
	}
	var found int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM download_requests WHERE id=? AND source_id='era5' AND collection_id=?`, requestID, collectionID).Scan(&found); err != nil {
		return err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO era5_result_manifest(
		downloader_task_id,request_id,job_id,collection_id,asset_id,filename,size,recorded_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(downloader_task_id) DO UPDATE SET job_id=excluded.job_id,
		asset_id=excluded.asset_id,filename=excluded.filename,size=excluded.size,recorded_at=excluded.recorded_at
		WHERE era5_result_manifest.request_id=excluded.request_id AND era5_result_manifest.collection_id=excluded.collection_id`,
		taskID, requestID, jobID, collectionID, assetID, filename, size, formatTime(time.Now()))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("ERA5 result task belongs to a different request")
	}
	if len(slices) > 0 {
		part := slices[0]
		encoded, err := json.Marshal(part.Submission.Inputs)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO era5_result_slices(downloader_task_id,start_ts,end_ts,inputs_json) VALUES(?,?,?,?)
  ON CONFLICT(downloader_task_id) DO UPDATE SET start_ts=excluded.start_ts,end_ts=excluded.end_ts,inputs_json=excluded.inputs_json`, taskID, formatTime(part.Start), formatTime(part.End), string(encoded)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ERA5ResultMatches(ctx context.Context, taskID int64, jobID, collection, filename string, size int64) (bool, error) {
	_, matched, err := s.ERA5ResultEvidence(ctx, taskID, jobID, collection, filename, size)
	return matched, err
}

func (s *Store) ERA5ResultEvidence(ctx context.Context, taskID int64, jobID, collection, filename string, size int64) (string, bool, error) {
	var assetID string
	err := s.db.QueryRowContext(ctx, `SELECT m.asset_id FROM era5_result_manifest m
		JOIN download_requests r ON r.id=m.request_id
		WHERE m.downloader_task_id=? AND m.job_id=? AND m.collection_id=? AND m.filename=? AND m.size=?
		AND r.source_id='era5' AND r.collection_id=m.collection_id`, taskID, jobID, collection, filename, size).Scan(&assetID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return assetID, err == nil, err
}

func (s *Store) EnqueueERA5Order(ctx context.Context, collection string, start, end time.Time, inputs map[string]json.RawMessage) (DownloadRequest, error) {
	return s.EnqueueERA5OrderOwned(ctx, "", collection, start, end, inputs)
}

func (s *Store) EnqueueERA5OrderOwned(ctx context.Context, ownerUserID, collection string, start, end time.Time, inputs map[string]json.RawMessage) (DownloadRequest, error) {
	if err := era5.ValidateOrder(collection, start, end, inputs); err != nil {
		return DownloadRequest{}, err
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return DownloadRequest{}, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return DownloadRequest{}, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	result, err := tx.ExecContext(ctx, `INSERT INTO download_requests(owner_user_id,source_id,provider_id,collection_id,start_ts,end_ts,cursor_ts,selection_json,state,requested_at,updated_at) VALUES(?,'era5','era5',?,?,?,?,'{}','pending',?,?)`, ownerUserID, collection, formatTime(start), formatTime(end), formatTime(start), now, now)
	if err != nil {
		return DownloadRequest{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return DownloadRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO era5_order_inputs(request_id,inputs_json) VALUES(?,?)`, id, string(encoded)); err != nil {
		return DownloadRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return DownloadRequest{}, err
	}
	return s.DownloadRequest(ctx, id)
}

func (s *Store) ERA5Inputs(ctx context.Context, id int64) (map[string]json.RawMessage, error) {
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT inputs_json FROM era5_order_inputs WHERE request_id=?`, id).Scan(&encoded); err != nil {
		return nil, err
	}
	var inputs map[string]json.RawMessage
	err := json.Unmarshal([]byte(encoded), &inputs)
	return inputs, err
}

func (s *Store) ERA5OrderPart(ctx context.Context, request DownloadRequest) (ERA5Part, error) {
	var part ERA5Part
	var start, end, encoded string
	err := s.db.QueryRowContext(ctx, `SELECT start_ts,end_ts,inputs_json,stage,job_id FROM era5_order_parts WHERE request_id=? AND start_ts=?`, request.ID, formatTime(request.Cursor)).Scan(&start, &end, &encoded, &part.Stage, &part.JobID)
	if err != nil {
		return part, err
	}
	part.Start, part.End = parseDownloadTime(start), parseDownloadTime(end)
	part.Submission.CollectionID = request.CollectionID
	err = json.Unmarshal([]byte(encoded), &part.Submission.Inputs)
	return part, err
}

func (s *Store) PrepareERA5Part(ctx context.Context, request DownloadRequest, submission era5.Submission, end time.Time) error {
	encoded, err := json.Marshal(submission.Inputs)
	if err != nil {
		return err
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO era5_order_parts(request_id,start_ts,end_ts,inputs_json,stage,job_id) VALUES(?,?,?,?,'pending','') ON CONFLICT(request_id) DO UPDATE SET start_ts=excluded.start_ts,end_ts=excluded.end_ts,inputs_json=excluded.inputs_json,stage='pending',job_id='' WHERE era5_order_parts.start_ts<>excluded.start_ts`, request.ID, formatTime(request.Cursor), formatTime(end), string(encoded))
	return err
}

func (s *Store) SetERA5PartStage(ctx context.Context, id int64, stage, jobID string) error {
	if stage != "pending" && stage != "submitting" && stage != "submitted" {
		return errors.New("invalid ERA5 stage")
	}
	result, err := s.writer.ExecContext(ctx, `UPDATE era5_order_parts SET stage=?,job_id=? WHERE request_id=?`, stage, jobID, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("ERA5 order part disappeared")
	}
	return nil
}

func (s *Store) FailDownloadOrder(ctx context.Context, id int64, cause error) error {
	now := formatTime(time.Now())
	_, err := s.writer.ExecContext(ctx, `UPDATE download_requests SET state='failed',last_error=?,completed_at=?,updated_at=? WHERE id=?`, truncateError(cause), now, now, id)
	return err
}

// Deferral is normal scheduler progress, not a failed attempt.
func (s *Store) DeferDownloadOrder(ctx context.Context, id int64, delay time.Duration) error {
	now := time.Now()
	_, err := s.writer.ExecContext(ctx, `UPDATE download_requests SET state='pending',next_attempt_ns=?,updated_at=? WHERE id=?`, now.Add(delay).UnixNano(), formatTime(now), id)
	return err
}
