package state

import (
	"context"
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
);`

type ERA5Part struct {
	Start, End   time.Time
	Submission   era5.Submission
	Stage, JobID string
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
	tx, err := s.db.BeginTx(ctx, nil)
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO era5_order_parts(request_id,start_ts,end_ts,inputs_json,stage,job_id) VALUES(?,?,?,?,'pending','') ON CONFLICT(request_id) DO UPDATE SET start_ts=excluded.start_ts,end_ts=excluded.end_ts,inputs_json=excluded.inputs_json,stage='pending',job_id='' WHERE era5_order_parts.start_ts<>excluded.start_ts`, request.ID, formatTime(request.Cursor), formatTime(end), string(encoded))
	return err
}

func (s *Store) SetERA5PartStage(ctx context.Context, id int64, stage, jobID string) error {
	if stage != "pending" && stage != "submitting" && stage != "submitted" {
		return errors.New("invalid ERA5 stage")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE era5_order_parts SET stage=?,job_id=? WHERE request_id=?`, stage, jobID, id)
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
	_, err := s.db.ExecContext(ctx, `UPDATE download_requests SET state='failed',last_error=?,completed_at=?,updated_at=? WHERE id=?`, truncateError(cause), now, now, id)
	return err
}

// Deferral is normal scheduler progress, not a failed attempt.
func (s *Store) DeferDownloadOrder(ctx context.Context, id int64, delay time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `UPDATE download_requests SET state='pending',next_attempt_ns=?,updated_at=? WHERE id=?`, now.Add(delay).UnixNano(), formatTime(now), id)
	return err
}
