package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

const DownloadTaskPageSize = 30

type DownloadTaskCursor struct {
	StampSec  string
	StampFrac string
	Kind      string
	ID        int64
}

// EncodeDownloadTaskCursor formats a keyset cursor aligned with the
// substr(stamp,1,19) / 9-digit fractional-second sort order used by task pages.
func EncodeDownloadTaskCursor(stamp, kind string, id int64) string {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" || (kind != "group" && kind != "legacy") || id <= 0 {
		return ""
	}
	var sec, frac string
	if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		utc := parsed.UTC()
		sec = utc.Format("2006-01-02T15:04:05")
		frac = fmt.Sprintf("%09d", utc.Nanosecond())
	} else {
		if len(stamp) < 19 {
			return ""
		}
		sec = stamp[:19]
		clean := strings.ReplaceAll(stamp, "Z", "") + "0000000000"
		if len(clean) < 29 {
			return ""
		}
		frac = clean[20:29]
	}
	return fmt.Sprintf("%s|%s|%s|%d", sec, frac, kind, id)
}

func ParseDownloadTaskCursor(raw string) (DownloadTaskCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DownloadTaskCursor{}, errors.New("empty task cursor")
	}
	parts := strings.Split(raw, "|")
	if len(parts) != 4 {
		return DownloadTaskCursor{}, errors.New("invalid task cursor")
	}
	sec, frac, kind, idText := parts[0], parts[1], parts[2], parts[3]
	if len(sec) != 19 || len(frac) != 9 || (kind != "group" && kind != "legacy") {
		return DownloadTaskCursor{}, errors.New("invalid task cursor")
	}
	if _, err := time.Parse("2006-01-02T15:04:05", sec); err != nil {
		return DownloadTaskCursor{}, errors.New("invalid task cursor")
	}
	for i := 0; i < len(frac); i++ {
		if frac[i] < '0' || frac[i] > '9' {
			return DownloadTaskCursor{}, errors.New("invalid task cursor")
		}
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return DownloadTaskCursor{}, errors.New("invalid task cursor")
	}
	return DownloadTaskCursor{StampSec: sec, StampFrac: frac, Kind: kind, ID: id}, nil
}

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
	Kind      string    `json:"kind"`
	Key       string    `json:"key"`
	RequestID int64     `json:"request_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Stamp     string    `json:"-"`
}

type downloadTaskRef struct {
	kind  string
	id    int64
	owner string
	stamp string
}

func downloadTasksUnionSQL(owner string, admin bool) (string, []any) {
	groupWhere, legacyWhere := "", ""
	var args []any
	if !admin {
		groupWhere = " WHERE owner_user_id=? AND owner_user_id<>''"
		legacyWhere = " AND r.owner_user_id=? AND r.owner_user_id<>''"
		args = append(args, owner, owner)
	}
	union := `SELECT 'group' AS kind,id,owner_user_id,created_at AS stamp FROM download_groups` + groupWhere + `
	 UNION ALL SELECT 'legacy' AS kind,r.id,r.owner_user_id,r.requested_at AS stamp FROM download_requests r
	 WHERE NOT EXISTS (SELECT 1 FROM download_group_members m WHERE m.request_id=r.id)` + legacyWhere
	return union, args
}

func (s *Store) hydrateDownloadTaskItems(ctx context.Context, refs []downloadTaskRef) ([]DownloadTaskListItem, error) {
	items := make([]DownloadTaskListItem, 0, len(refs))
	for _, ref := range refs {
		item := DownloadTaskListItem{Kind: ref.kind, Key: fmt.Sprintf("%s:%d", ref.kind, ref.id), Stamp: ref.stamp}
		if ref.kind == "group" {
			task, err := s.DownloadGroupOwned(ctx, ref.id, ref.owner)
			if err != nil {
				return nil, err
			}
			item.DownloadGroup = task
		} else {
			request, err := scanDownloadRequest(s.db.QueryRowContext(ctx, downloadRequestSelect+` WHERE id=? AND owner_user_id=? AND NOT EXISTS (SELECT 1 FROM download_group_members WHERE request_id=download_requests.id)`, ref.id, ref.owner))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			item.RequestID = request.ID
			item.DownloadGroup = DownloadGroup{ID: request.ID, State: request.State, Products: 1, AssetsQueued: request.AssetsQueued, FilesDone: request.FilesDone, FilesFailed: request.FilesFailed, FilesWaitingAuth: request.FilesWaitingAuth, CreatedAt: request.RequestedAt, Downloads: []DownloadRequest{request}}
		}
		item.UpdatedAt = item.CreatedAt
		for _, dl := range item.Downloads {
			if dl.UpdatedAt.After(item.UpdatedAt) {
				item.UpdatedAt = dl.UpdatedAt
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// DownloadTasksPage projects ungrouped requests without changing ownership or history.
func (s *Store) DownloadTasksPage(ctx context.Context, page int, owner string, admin bool) ([]DownloadTaskListItem, int, error) {
	if page < 1 || page > 1000000 {
		return nil, 0, errors.New("invalid task page")
	}
	union, args := downloadTasksUnionSQL(owner, admin)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+union+") task_items", args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), DownloadTaskPageSize, (page-1)*DownloadTaskPageSize)
	rows, err := s.db.QueryContext(ctx, "SELECT kind,id,owner_user_id,stamp FROM ("+union+") task_items ORDER BY substr(stamp,1,19) DESC,substr(replace(stamp,'Z','') || '0000000000',21,9) DESC,kind DESC,id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	var refs []downloadTaskRef
	for rows.Next() {
		var item downloadTaskRef
		if err := rows.Scan(&item.kind, &item.id, &item.owner, &item.stamp); err != nil {
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
	items, err := s.hydrateDownloadTaskItems(ctx, refs)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// DownloadTasksCursorPage returns a keyset-paginated slice of download tasks.
func (s *Store) DownloadTasksCursorPage(ctx context.Context, cursor *DownloadTaskCursor, limit int, owner string, admin bool) ([]DownloadTaskListItem, int, bool, string, error) {
	if limit <= 0 {
		limit = DownloadTaskPageSize
	}
	if limit > 200 {
		limit = 200
	}
	union, args := downloadTasksUnionSQL(owner, admin)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ("+union+") task_items", args...).Scan(&total); err != nil {
		return nil, 0, false, "", err
	}
	base := "SELECT kind,id,owner_user_id,stamp,substr(stamp,1,19) AS sec,substr(replace(stamp,'Z','') || '0000000000',21,9) AS frac FROM (" + union + ") task_items"
	queryArgs := append([]any{}, args...)
	query := "SELECT kind,id,owner_user_id,stamp FROM (" + base + ") keyed"
	if cursor != nil {
		query += " WHERE (sec < ? OR (sec = ? AND (frac < ? OR (frac = ? AND (kind < ? OR (kind = ? AND id < ?))))))"
		queryArgs = append(queryArgs, cursor.StampSec, cursor.StampSec, cursor.StampFrac, cursor.StampFrac, cursor.Kind, cursor.Kind, cursor.ID)
	}
	query += " ORDER BY sec DESC,frac DESC,kind DESC,id DESC LIMIT ?"
	queryArgs = append(queryArgs, limit+1)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, false, "", err
	}
	var refs []downloadTaskRef
	for rows.Next() {
		var item downloadTaskRef
		if err := rows.Scan(&item.kind, &item.id, &item.owner, &item.stamp); err != nil {
			rows.Close()
			return nil, 0, false, "", err
		}
		refs = append(refs, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, false, "", err
	}
	hasMore := false
	if len(refs) > limit {
		hasMore = true
		refs = refs[:limit]
	}
	items, err := s.hydrateDownloadTaskItems(ctx, refs)
	if err != nil {
		return nil, 0, false, "", err
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = EncodeDownloadTaskCursor(last.Stamp, last.Kind, last.ID)
	}
	return items, total, hasMore, nextCursor, nil
}
