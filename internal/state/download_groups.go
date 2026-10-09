package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/era5"
)

const downloadGroupsSchema = `
CREATE TABLE IF NOT EXISTS download_groups(
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 owner_user_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(owner_user_id,fingerprint)
);
CREATE TABLE IF NOT EXISTS download_group_members(
 group_id INTEGER NOT NULL REFERENCES download_groups(id) ON DELETE CASCADE,
 request_id INTEGER NOT NULL REFERENCES download_requests(id) ON DELETE CASCADE,
 PRIMARY KEY(group_id,request_id)
);
CREATE INDEX IF NOT EXISTS download_group_members_request_idx
ON download_group_members(request_id);
`

// GroupProduct is an already validated, normalized satellite product order.
// The API validates every member before beginning a single SQLite transaction:
// one rejected product can never create a partially submitted group.
type GroupProduct struct {
	Source     string                     `json:"source"`
	Provider   string                     `json:"provider"`
	Collection string                     `json:"collection"`
	Selection  downloadfilter.Selection   `json:"selection"`
	Inputs     map[string]json.RawMessage `json:"inputs,omitempty"`
}

type DownloadGroup struct {
	ID               int64             `json:"id"`
	State            string            `json:"state"`
	Products         int               `json:"products"`
	AssetsQueued     int               `json:"assets_queued"`
	FilesDone        int               `json:"files_done"`
	FilesFailed      int               `json:"files_failed"`
	FilesWaitingAuth int               `json:"files_waiting_auth"`
	CreatedAt        time.Time         `json:"created_at"`
	Downloads        []DownloadRequest `json:"downloads"`
}

func (s *Store) EnqueueDownloadGroup(ctx context.Context, owner string, start, end time.Time, specs []GroupProduct) (DownloadGroup, bool, error) {
	if owner == "" || len(specs) == 0 || len(specs) > 32 || !end.After(start) {
		return DownloadGroup{}, false, errors.New("invalid group submission")
	}
	ordered := append([]GroupProduct(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Collection < ordered[j].Collection })
	for i, spec := range ordered {
		if spec.Collection == "" || spec.Provider == "" || spec.Source == "" ||
			(i > 0 && spec.Collection == ordered[i-1].Collection) {
			return DownloadGroup{}, false, errors.New("invalid or duplicated product in group")
		}
	}
	key, err := json.Marshal(struct {
		Start    string         `json:"start"`
		End      string         `json:"end"`
		Products []GroupProduct `json:"products"`
	}{formatTime(start), formatTime(end), ordered})
	if err != nil {
		return DownloadGroup{}, false, err
	}
	fingerprint := sha256.Sum256(key)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return DownloadGroup{}, false, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	result, err := tx.ExecContext(ctx, `INSERT INTO download_groups(owner_user_id,fingerprint,created_at)
	 VALUES(?,?,?) ON CONFLICT(owner_user_id,fingerprint) DO NOTHING`,
		owner, hex.EncodeToString(fingerprint[:]), now)
	if err != nil {
		return DownloadGroup{}, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return DownloadGroup{}, false, err
	}
	var groupID int64
	if n == 0 {
		err = tx.QueryRowContext(ctx, `SELECT id FROM download_groups WHERE owner_user_id=? AND fingerprint=?`,
			owner, hex.EncodeToString(fingerprint[:])).Scan(&groupID)
		if err != nil {
			return DownloadGroup{}, false, err
		}
	} else {
		groupID, err = result.LastInsertId()
		if err != nil {
			return DownloadGroup{}, false, err
		}
		for _, spec := range ordered {
			selection, err := json.Marshal(spec.Selection)
			if err != nil {
				return DownloadGroup{}, false, err
			}
			created, err := tx.ExecContext(ctx, `
				INSERT INTO download_requests(owner_user_id,source_id,provider_id,collection_id,
				start_ts,end_ts,cursor_ts,selection_json,state,requested_at,updated_at)
				VALUES(?,?,?,?,?,?,?,?,'pending',?,?)`,
				owner, spec.Source, spec.Provider, spec.Collection,
				formatTime(start), formatTime(end), formatTime(start), string(selection), now, now)
			if err != nil {
				return DownloadGroup{}, false, err
			}
			id, err := created.LastInsertId()
			if err != nil {
				return DownloadGroup{}, false, err
			}
			if spec.Source == "era5" {
				if err := era5.ValidateOrder(spec.Collection, start, end, spec.Inputs); err != nil {
					return DownloadGroup{}, false, err
				}
				data, err := json.Marshal(spec.Inputs)
				if err != nil {
					return DownloadGroup{}, false, err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO era5_order_inputs(request_id,inputs_json) VALUES(?,?)`, id, string(data)); err != nil {
					return DownloadGroup{}, false, err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO download_group_members(group_id,request_id) VALUES(?,?)`, groupID, id); err != nil {
				return DownloadGroup{}, false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return DownloadGroup{}, false, err
	}
	group, err := s.DownloadGroupOwned(ctx, groupID, owner)
	return group, n == 1, err
}

func (s *Store) DownloadGroupOwned(ctx context.Context, id int64, owner string) (DownloadGroup, error) {
	var group DownloadGroup
	var stamp string
	err := s.db.QueryRowContext(ctx, `SELECT id,created_at FROM download_groups WHERE id=? AND owner_user_id=?`, id, owner).Scan(&group.ID, &stamp)
	if err != nil {
		return group, err
	}
	group.CreatedAt = parseDownloadTime(stamp)
	rows, err := s.db.QueryContext(ctx, downloadRequestSelect+`
	 WHERE id IN (SELECT request_id FROM download_group_members WHERE group_id=?)
	 ORDER BY id`, id)
	if err != nil {
		return group, err
	}
	defer rows.Close()
	group.Downloads = []DownloadRequest{}
	terminal := true
	failed := false
	waiting := false
	for rows.Next() {
		item, err := scanDownloadRequest(rows)
		if err != nil {
			return group, err
		}
		group.Downloads = append(group.Downloads, item)
		group.AssetsQueued += item.AssetsQueued
		group.FilesDone += item.FilesDone
		group.FilesFailed += item.FilesFailed
		group.FilesWaitingAuth += item.FilesWaitingAuth
		if item.State != "done" && item.State != "failed" {
			terminal = false
		}
		if item.State == "failed" || item.FilesFailed > 0 {
			failed = true
		}
		if item.State == "waiting_auth" || item.FilesWaitingAuth > 0 {
			waiting = true
		}
	}
	if err := rows.Err(); err != nil {
		return group, err
	}
	group.Products = len(group.Downloads)
	if group.Products == 0 {
		return group, fmt.Errorf("download group %d missing child orders", id)
	}
	switch {
	case terminal && failed:
		group.State = "failed"
	case terminal:
		group.State = "done"
	case waiting:
		group.State = "waiting_auth"
	default:
		group.State = "running"
	}
	return group, nil
}

func (s *Store) DownloadGroupsByOwner(ctx context.Context, owner string, limit int) ([]DownloadGroup, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM download_groups WHERE owner_user_id=? ORDER BY id DESC LIMIT ?`, owner, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]DownloadGroup, 0, len(ids))
	for _, id := range ids {
		item, err := s.DownloadGroupOwned(ctx, id, owner)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}
