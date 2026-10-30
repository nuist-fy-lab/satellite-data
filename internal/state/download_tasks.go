package state

import (
	"context"
	"encoding/json"
	"errors"

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

// DownloadTasksPage lists submitted tasks, rather than their per-product work
// items. Unassociated legacy requests are deliberately excluded.
func (s *Store) DownloadTasksPage(ctx context.Context, page int, owner string, admin bool) ([]DownloadGroup, int, error) {
	if page < 1 || page > 1000000 {
		return nil, 0, errors.New("invalid task page")
	}
	where := ""
	args := []any{}
	if !admin {
		where = " WHERE owner_user_id=?"
		args = append(args, owner)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM download_groups"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), DownloadTaskPageSize, (page-1)*DownloadTaskPageSize)
	rows, err := s.db.QueryContext(ctx, "SELECT id,owner_user_id FROM download_groups"+where+" ORDER BY id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	type ref struct {
		id    int64
		owner string
	}
	var refs []ref
	for rows.Next() {
		var item ref
		if err := rows.Scan(&item.id, &item.owner); err != nil {
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
	items := make([]DownloadGroup, 0, len(refs))
	for _, ref := range refs {
		task, err := s.DownloadGroupOwned(ctx, ref.id, ref.owner)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, task)
	}
	return items, total, nil
}
