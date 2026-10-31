package state

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestRegionalOfficialProgressResumeAndAtomicPublish(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const key = "himawari9:AHI-L1b-Japan"
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.UpsertProducts(ctx, "himawari9", []model.Product{{ProductKey: key, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	old := model.Granule{ID: "old", Start: day, Assets: []model.Asset{{Name: "old.hsd"}}}
	if err := s.SaveOfficialBatch(ctx, WorkBatch{Product: key, Days: []string{"2024-01-01"}}, []model.Granule{old}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE official_days SET refreshed_at='2000-01-01T00:00:00Z' WHERE product_key=?`, key); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueRange(ctx, key, day, day.AddDate(0, 0, 1), 10); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimBatch(ctx, 7)
	if err != nil || len(claim.Days) != 1 {
		t.Fatalf("claim %+v %v", claim, err)
	}
	first := model.Granule{ID: "scene", Start: day.Add(2 * time.Minute), End: day.Add(8 * time.Minute), Assets: []model.Asset{{Name: "removed.hsd"}, {Name: "scene.hsd", ETag: "old"}}, Metadata: map[string]string{"observation_start_mjd": "first-start", "observation_end_mjd": "first-end"}}
	moved := model.Granule{ID: "moved", Start: day.Add(3 * time.Minute), Assets: []model.Asset{{Name: "moved.hsd"}}}
	start, end, err := s.RegionalOfficialWindow(ctx, claim)
	if err != nil || !start.Equal(day) || end.Sub(start) != 10*time.Minute {
		t.Fatalf("window %s %s %v", start, end, err)
	}
	if done, err := s.SaveRegionalOfficialWindow(ctx, claim, start, []model.Granule{first, first, moved}); err != nil || done {
		t.Fatalf("save %v %v", done, err)
	}
	var original int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_slots WHERE product_key=? AND granule_id='old'`, key).Scan(&original); err != nil || original != 1 {
		t.Fatalf("original day changed: %d %v", original, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RecoverOfficialWork(ctx); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	start, _, err = s.RegionalOfficialWindow(ctx, claim)
	if err != nil || !start.Equal(day.Add(10*time.Minute)) {
		t.Fatalf("resume %s %v", start, err)
	}
	stale := claim
	if err := s.MarkBatchError(ctx, claim, errors.New("upstream failure")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE official_work SET next_attempt_ns=0 WHERE product_key=? AND day=?`, key, claim.Days[0]); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRegionalOfficialWindow(ctx, stale, start, nil); err == nil {
		t.Fatal("stale claim accepted")
	}
	for window := 1; window < 144; window++ {
		start, _, err = s.RegionalOfficialWindow(ctx, claim)
		if err != nil || !start.Equal(day.Add(time.Duration(window)*10*time.Minute)) {
			t.Fatalf("window %d: %s %v", window, start, err)
		}
		var items []model.Granule
		if window == 1 {
			items = []model.Granule{{ID: first.ID, Start: day.Add(4 * time.Minute), End: day.Add(5 * time.Minute), Assets: []model.Asset{{Name: "scene.hsd", ETag: "new"}}, Metadata: map[string]string{"observation_start_mjd": "latest-start", "observation_end_mjd": "latest-end"}}, {ID: moved.ID, Start: day.AddDate(0, 0, 1), Assets: moved.Assets}}
		}
		if window == 143 {
			if _, err := s.writer.ExecContext(ctx, `CREATE TRIGGER fail_regional_publish BEFORE DELETE ON official_work BEGIN SELECT RAISE(ABORT,'simulated publish failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SaveRegionalOfficialWindow(ctx, claim, start, items); err == nil {
				t.Fatal("expected publish failure")
			}
			var next, oldCount int
			if err := s.db.QueryRowContext(ctx, `SELECT next_window FROM regional_official_progress WHERE product_key=? AND day=?`, key, claim.Days[0]).Scan(&next); err != nil || next != 143 {
				t.Fatalf("progress after rollback %d %v", next, err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_slots WHERE product_key=? AND granule_id='old'`, key).Scan(&oldCount); err != nil || oldCount != 1 {
				t.Fatalf("old day after rollback %d %v", oldCount, err)
			}
			if _, err := s.writer.ExecContext(ctx, `DROP TRIGGER fail_regional_publish`); err != nil {
				t.Fatal(err)
			}
		}
		done, err := s.SaveRegionalOfficialWindow(ctx, claim, start, items)
		if err != nil || done != (window == 143) {
			t.Fatalf("save %d: %v %v", window, done, err)
		}
		if window == 1 {
			var encoded string
			if err := s.db.QueryRowContext(ctx, `SELECT granules_json FROM regional_official_progress WHERE product_key=? AND day=?`, key, claim.Days[0]).Scan(&encoded); err != nil {
				t.Fatal(err)
			}
			var staged []model.Granule
			if err := json.Unmarshal([]byte(encoded), &staged); err != nil {
				t.Fatal(err)
			}
			if len(staged) != 2 {
				t.Fatalf("staged duplicate: %+v", staged)
			}
			for _, g := range staged {
				if g.ID == first.ID && (!g.Start.Equal(day.Add(4*time.Minute)) || !g.End.Equal(day.Add(5*time.Minute)) || g.Metadata["observation_start_mjd"] != "latest-start" || g.Metadata["observation_end_mjd"] != "latest-end" || len(g.Assets) != 1 || g.Assets[0].ETag != "new") {
					t.Fatalf("latest snapshot: %+v", g)
				}
			}
		}
		if !done {
			claim, err = s.ClaimBatch(ctx, 7)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var count, oldCount, staged int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),sum(CASE WHEN granule_id='old' THEN 1 ELSE 0 END) FROM official_slots WHERE product_key=?`, key).Scan(&count, &oldCount); err != nil || count != 1 || oldCount != 0 {
		t.Fatalf("published count=%d old=%d err=%v", count, oldCount, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM regional_official_progress WHERE product_key=?`, key).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("staging=%d err=%v", staged, err)
	}
	var assets int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_assets WHERE product_key=? AND granule_id='scene' AND filename='scene.hsd' AND etag='new'`, key).Scan(&assets); err != nil || assets != 1 {
		t.Fatalf("latest assets=%d err=%v", assets, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_assets WHERE product_key=? AND granule_id='scene'`, key).Scan(&assets); err != nil || assets != 1 {
		t.Fatalf("removed assets remain: %d err=%v", assets, err)
	}
	var slot string
	if err := s.db.QueryRowContext(ctx, `SELECT slot_ts FROM official_slots WHERE product_key=? AND granule_id='scene'`, key).Scan(&slot); err != nil || slot != day.Add(4*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("latest start=%s err=%v", slot, err)
	}
}

func TestRegionalOfficialClaimIsExclusive(t *testing.T) {
	ctx := t.Context()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const key = "himawari8:AHI-L1b-Target"
	if err := s.UpsertProducts(ctx, "himawari8", []model.Product{{ProductKey: key, SourceID: "himawari8"}}); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.EnqueueRange(ctx, key, day, day.AddDate(0, 0, 1), 10); err != nil {
		t.Fatal(err)
	}
	type result struct {
		batch WorkBatch
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { b, e := s.ClaimBatch(ctx, 7); results <- result{b, e} }()
	}
	claimed := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		claimed += len(r.batch.Days)
	}
	if claimed != 1 {
		t.Fatalf("claimed %d times", claimed)
	}
}

func TestRegionalOfficialProductScope(t *testing.T) {
	for _, product := range []string{"himawari8:AHI-L1b-Japan", "himawari9:AHI-L1b-Target"} {
		if !IsRegionalOfficialProduct(product) {
			t.Fatal(product)
		}
	}
	for _, product := range []string{"himawari8:AHI-L1b-FLDK", "himawari9:AHI-L2-Japan", "other:AHI-L1b-Japan"} {
		if IsRegionalOfficialProduct(product) {
			t.Fatal(product)
		}
	}
}
