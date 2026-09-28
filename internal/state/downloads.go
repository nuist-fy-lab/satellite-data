package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
)

const downloadSchema = `
CREATE TABLE IF NOT EXISTS download_requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_id TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  collection_id TEXT NOT NULL,
  start_ts TEXT NOT NULL,
  end_ts TEXT NOT NULL,
  cursor_ts TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  granules_discovered INTEGER NOT NULL DEFAULT 0,
  assets_queued INTEGER NOT NULL DEFAULT 0,
  files_done INTEGER NOT NULL DEFAULT 0,
  files_failed INTEGER NOT NULL DEFAULT 0,
  files_waiting_auth INTEGER NOT NULL DEFAULT 0,
  failure_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_ns INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  requested_at TEXT NOT NULL,
  completed_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS download_requests_ready_idx
ON download_requests(provider_id,state,next_attempt_ns,id);

CREATE TABLE IF NOT EXISTS download_request_tasks (
  request_id INTEGER NOT NULL REFERENCES download_requests(id) ON DELETE CASCADE,
  downloader_task_id INTEGER NOT NULL,
  task_key TEXT NOT NULL DEFAULT '',
  filename TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(request_id,downloader_task_id)
);
CREATE INDEX IF NOT EXISTS download_request_tasks_task_idx
ON download_request_tasks(downloader_task_id,request_id);
`

type DownloadRequest struct {
	ID                 int64                    `json:"id"`
	OwnerUserID        string                   `json:"-"`
	SourceID           string                   `json:"source_id"`
	ProviderID         string                   `json:"provider_id"`
	CollectionID       string                   `json:"collection_id"`
	Selection          downloadfilter.Selection `json:"selection"`
	Start              time.Time                `json:"start"`
	End                time.Time                `json:"end"`
	Cursor             time.Time                `json:"cursor"`
	State              string                   `json:"state"`
	GranulesDiscovered int                      `json:"granules_discovered"`
	AssetsQueued       int                      `json:"assets_queued"`
	FilesDone          int                      `json:"files_done"`
	FilesFailed        int                      `json:"files_failed"`
	FilesWaitingAuth   int                      `json:"files_waiting_auth"`
	FailureCount       int                      `json:"failure_count"`
	LastError          string                   `json:"last_error,omitempty"`
	RequestedAt        time.Time                `json:"requested_at"`
	CompletedAt        time.Time                `json:"completed_at,omitempty"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

type DownloaderTaskRef struct {
	ID       int64
	Key      string
	Filename string
}

func initDownloadSchema(db *sql.DB) error {
	_, err := db.Exec(downloadSchema)
	if err != nil {
		return err
	}
	if _, err := db.Exec(era5OrderSchema); err != nil {
		return err
	}
	rows, err := db.Query(`PRAGMA table_info(download_requests)`)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		found[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found["selection_json"] {
		if _, err := db.Exec(`ALTER TABLE download_requests ADD COLUMN selection_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return err
		}
	}
	if !found["owner_user_id"] {
		if _, err := db.Exec(`ALTER TABLE download_requests ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS download_requests_owner_idx ON download_requests(owner_user_id,id)`)
	return err
}

func (s *Store) RecoverDownloadRequests(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		UPDATE download_requests
		SET state='pending',last_error='recovered after satellite-data restart',updated_at=?
		WHERE state='discovering'`, now)
	return err
}

func (s *Store) EnqueueDownloadRequest(ctx context.Context, sourceID, providerID, collectionID string, start, end time.Time) (DownloadRequest, error) {
	return s.EnqueueSelectedDownloadRequest(ctx, sourceID, providerID, collectionID, start, end, downloadfilter.Selection{})
}

func (s *Store) EnqueueSelectedDownloadRequest(ctx context.Context, sourceID, providerID, collectionID string, start, end time.Time, selection downloadfilter.Selection) (DownloadRequest, error) {
	return s.EnqueueSelectedDownloadRequestOwned(ctx, "", sourceID, providerID, collectionID, start, end, selection)
}

func (s *Store) EnqueueSelectedDownloadRequestOwned(ctx context.Context, ownerUserID, sourceID, providerID, collectionID string, start, end time.Time, selection downloadfilter.Selection) (DownloadRequest, error) {
	sourceID = strings.TrimSpace(sourceID)
	providerID = strings.TrimSpace(providerID)
	collectionID = strings.TrimSpace(collectionID)
	if sourceID == "" || providerID == "" || collectionID == "" {
		return DownloadRequest{}, errors.New("source, provider and collection are required")
	}
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return DownloadRequest{}, errors.New("end must be after start")
	}
	selection, err := selection.Normalize(sourceID, collectionID)
	if err != nil {
		return DownloadRequest{}, err
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		return DownloadRequest{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO download_requests(
		 owner_user_id,source_id,provider_id,collection_id,start_ts,end_ts,cursor_ts,selection_json,state,requested_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,'pending',?,?)`,
		ownerUserID, sourceID, providerID, collectionID, formatTime(start), formatTime(end), formatTime(start), string(encoded), formatTime(now), formatTime(now))
	if err != nil {
		return DownloadRequest{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return DownloadRequest{}, err
	}
	return s.DownloadRequest(ctx, id)
}

func (s *Store) ClaimDownloadRequest(ctx context.Context, providerID string) (DownloadRequest, bool, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DownloadRequest{}, false, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM download_requests
		WHERE provider_id=? AND state IN ('pending','retry_wait') AND next_attempt_ns<=?
		ORDER BY next_attempt_ns,id LIMIT 1`, providerID, now.UnixNano()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return DownloadRequest{}, false, nil
	}
	if err != nil {
		return DownloadRequest{}, false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE download_requests SET state='discovering',last_error='',updated_at=?
		WHERE id=? AND state IN ('pending','retry_wait')`, formatTime(now), id)
	if err != nil {
		return DownloadRequest{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return DownloadRequest{}, false, err
	}
	if changed != 1 {
		return DownloadRequest{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return DownloadRequest{}, false, err
	}
	request, err := s.DownloadRequest(ctx, id)
	return request, err == nil, err
}

func (s *Store) AdvanceDownloadRequest(ctx context.Context, id int64, nextCursor time.Time, granules int, refs []DownloaderTaskRef, discoveryDone bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	added := 0
	for _, ref := range refs {
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO download_request_tasks(request_id,downloader_task_id,task_key,filename)
			VALUES(?,?,?,?)`, id, ref.ID, ref.Key, ref.Filename)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		added += int(rows)
	}
	now := time.Now().UTC()
	state := "pending"
	completed := ""
	if discoveryDone {
		var taskCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM download_request_tasks WHERE request_id=?`, id).Scan(&taskCount); err != nil {
			return err
		}
		if taskCount == 0 {
			state = "done"
			completed = formatTime(now)
		} else {
			state = "transferring"
		}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE download_requests
		SET cursor_ts=?,state=?,granules_discovered=granules_discovered+?,assets_queued=assets_queued+?,
		    failure_count=0,next_attempt_ns=?,last_error='',completed_at=?,updated_at=?
		WHERE id=?`,
		formatTime(nextCursor), state, granules, added, now.UnixNano(), completed, formatTime(now), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetryDownloadDiscovery(ctx context.Context, id int64, cause error, delay time.Duration) error {
	if delay < time.Second {
		delay = time.Second
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE download_requests
		SET state='retry_wait',failure_count=failure_count+1,next_attempt_ns=?,last_error=?,updated_at=?
		WHERE id=?`, now.Add(delay).UnixNano(), truncateError(cause), formatTime(now), id)
	return err
}

func (s *Store) DownloadRequest(ctx context.Context, id int64) (DownloadRequest, error) {
	return scanDownloadRequest(s.db.QueryRowContext(ctx, downloadRequestSelect+` WHERE id=?`, id))
}

func (s *Store) DownloadRequestOwned(ctx context.Context, id int64, ownerUserID string) (DownloadRequest, error) {
	return scanDownloadRequest(s.db.QueryRowContext(ctx, downloadRequestSelect+` WHERE id=? AND owner_user_id=?`, id, ownerUserID))
}

func (s *Store) DownloadRequests(ctx context.Context, limit int) ([]DownloadRequest, error) {
	return s.downloadRequests(ctx, limit, "", false)
}

func (s *Store) DownloadRequestsOwned(ctx context.Context, limit int, ownerUserID string) ([]DownloadRequest, error) {
	return s.downloadRequests(ctx, limit, ownerUserID, true)
}

func (s *Store) downloadRequests(ctx context.Context, limit int, ownerUserID string, scoped bool) ([]DownloadRequest, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	query := downloadRequestSelect + ` ORDER BY id DESC LIMIT ?`
	args := []any{limit}
	if scoped {
		query = downloadRequestSelect + ` WHERE owner_user_id=? ORDER BY id DESC LIMIT ?`
		args = []any{ownerUserID, limit}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DownloadRequest, 0, limit)
	for rows.Next() {
		item, err := scanDownloadRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ActiveDownloadRequests(ctx context.Context, limit int) ([]DownloadRequest, error) {
	if limit < 1 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, downloadRequestSelect+`
		WHERE state IN ('pending','discovering','retry_wait','transferring','waiting_auth')
		ORDER BY CASE WHEN state IN ('transferring','waiting_auth') THEN 0 ELSE 1 END,updated_at,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DownloadRequest, 0, limit)
	for rows.Next() {
		item, err := scanDownloadRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DownloadRequestTaskIDs(ctx context.Context, requestID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT downloader_task_id FROM download_request_tasks WHERE request_id=? ORDER BY downloader_task_id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) UpdateDownloadTransferState(ctx context.Context, requestID int64, total int, tasks []downloader.Task) error {
	counts := map[string]int{}
	lastError := ""
	for _, task := range tasks {
		counts[task.State]++
		if task.LastError != "" {
			lastError = task.LastError
		}
	}
	done := counts["done"]
	failed := counts["failed"]
	waitingAuth := counts["waiting_auth"]
	known := len(tasks)
	state := "transferring"
	completed := ""
	if total == 0 {
		state = "done"
		completed = formatTime(time.Now())
	} else if known == total && done == total {
		state = "done"
		completed = formatTime(time.Now())
	} else if known == total && done+failed == total && failed > 0 {
		state = "failed"
		completed = formatTime(time.Now())
	} else if known == total && waitingAuth > 0 && done+waitingAuth == total {
		state = "waiting_auth"
	}
	now := formatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE download_requests
		SET state=CASE WHEN state IN ('pending','discovering','retry_wait') THEN state ELSE ? END,
		    files_done=?,files_failed=?,files_waiting_auth=?,
		    last_error=CASE WHEN state IN ('pending','discovering','retry_wait') THEN last_error ELSE ? END,
		    completed_at=CASE WHEN state IN ('pending','discovering','retry_wait') THEN completed_at ELSE ? END,
		    updated_at=?
		WHERE id=? AND state IN ('pending','discovering','retry_wait','transferring','waiting_auth')`,
		state, done, failed, waitingAuth, lastError, completed, now, requestID)
	return err
}

const downloadRequestSelect = `
SELECT id,source_id,provider_id,collection_id,start_ts,end_ts,cursor_ts,state,
       granules_discovered,assets_queued,files_done,files_failed,files_waiting_auth,
       failure_count,last_error,requested_at,completed_at,updated_at,selection_json,owner_user_id
FROM download_requests`

func scanDownloadRequest(scanner interface{ Scan(...any) error }) (DownloadRequest, error) {
	var item DownloadRequest
	var start, end, cursor, requested, completed, updated, selectionJSON string
	err := scanner.Scan(
		&item.ID, &item.SourceID, &item.ProviderID, &item.CollectionID,
		&start, &end, &cursor, &item.State, &item.GranulesDiscovered, &item.AssetsQueued,
		&item.FilesDone, &item.FilesFailed, &item.FilesWaitingAuth, &item.FailureCount,
		&item.LastError, &requested, &completed, &updated, &selectionJSON, &item.OwnerUserID,
	)
	if err != nil {
		return DownloadRequest{}, err
	}
	if err := json.Unmarshal([]byte(selectionJSON), &item.Selection); err != nil {
		return DownloadRequest{}, err
	}
	item.Start = parseDownloadTime(start)
	item.End = parseDownloadTime(end)
	item.Cursor = parseDownloadTime(cursor)
	item.RequestedAt = parseDownloadTime(requested)
	item.CompletedAt = parseDownloadTime(completed)
	item.UpdatedAt = parseDownloadTime(updated)
	return item, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseDownloadTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed.UTC()
}

func truncateError(cause error) string {
	if cause == nil {
		return ""
	}
	value := cause.Error()
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}

func (r DownloadRequest) ValidateProvider(sourceID, collectionID string) error {
	if r.SourceID != sourceID || r.CollectionID != collectionID {
		return fmt.Errorf("download request source/collection mismatch")
	}
	return nil
}
