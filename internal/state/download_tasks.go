package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

const DownloadTaskPageSize = 30

// Task summaries need catalogue labels, without aggregating the archive's
// potentially millions of local slots on every progress refresh.
func (s *Store) DownloadTaskProducts(ctx context.Context) ([]model.Product, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_key,source_id,satellite,instrument,data_type,display_name,level,area_type,resolution,version,cadence,metadata_json FROM products ORDER BY product_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Product, 0)
	for rows.Next() {
		var p model.Product
		var metadata string
		if err := rows.Scan(&p.ProductKey, &p.SourceID, &p.Satellite, &p.Instrument, &p.DataType, &p.DisplayName, &p.Level, &p.AreaType, &p.Resolution, &p.Version, &p.Cadence, &metadata); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadata), &p.Metadata); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

type DownloadTaskListItem struct {
	DownloadGroup
	Kind      string `json:"kind"`
	Key       string `json:"key"`
	RequestID int64  `json:"request_id,omitempty"`
}

// DownloadTasksPage projects ungrouped requests without changing ownership or history.
func (s *Store) DownloadTasksPage(ctx context.Context, page int, owner string, admin bool) ([]DownloadTaskListItem, int, error) {
	if page < 1 || page > 1000000 {
		return nil, 0, errors.New("invalid task page")
	}
	groupWhere, legacyWhere := "", ""
	args := []any{}
	if !admin {
		groupWhere = " WHERE owner_user_id=? AND owner_user_id<>''"
		legacyWhere = " AND r.owner_user_id=? AND r.owner_user_id<>''"
		args = append(args, owner, owner)
	}
	union := `SELECT 'group' AS kind,id,owner_user_id,created_at AS stamp FROM download_groups` + groupWhere + `
	 UNION ALL SELECT 'legacy' AS kind,r.id,r.owner_user_id,r.requested_at AS stamp FROM download_requests r
	 WHERE NOT EXISTS (SELECT 1 FROM download_group_members m WHERE m.request_id=r.id)` + legacyWhere
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+union+") task_items", args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), DownloadTaskPageSize, (page-1)*DownloadTaskPageSize)
	rows, err := s.db.QueryContext(ctx, "SELECT kind,id,owner_user_id FROM ("+union+") task_items ORDER BY substr(stamp,1,19) DESC,substr(replace(stamp,'Z','') || '000000000',21,9) DESC,kind DESC,id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	type ref struct {
		kind  string
		id    int64
		owner string
	}
	var refs []ref
	for rows.Next() {
		var item ref
		if err := rows.Scan(&item.kind, &item.id, &item.owner); err != nil {
			rows.Close()
			return nil, 0, err
		}
		refs = append(refs, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	items := make([]DownloadTaskListItem, 0, len(refs))
	for _, ref := range refs {
		item := DownloadTaskListItem{Kind: ref.kind, Key: fmt.Sprintf("%s:%d", ref.kind, ref.id)}
		if ref.kind == "group" {
			task, err := s.DownloadGroupOwned(ctx, ref.id, ref.owner)
			if err != nil {
				return nil, 0, err
			}
			item.DownloadGroup = task
		} else {
			request, err := scanDownloadRequest(s.db.QueryRowContext(ctx, downloadRequestSelect+` WHERE id=? AND owner_user_id=? AND NOT EXISTS (SELECT 1 FROM download_group_members WHERE request_id=download_requests.id)`, ref.id, ref.owner))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, 0, err
			}
			item.RequestID = request.ID
			item.DownloadGroup = DownloadGroup{ID: request.ID, State: request.State, Products: 1, AssetsQueued: request.AssetsQueued, FilesDone: request.FilesDone, FilesFailed: request.FilesFailed, FilesWaitingAuth: request.FilesWaitingAuth, CreatedAt: request.RequestedAt, Downloads: []DownloadRequest{request}}
		}
		items = append(items, item)
	}
	return items, total, nil
}
