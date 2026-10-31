package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

// SATELLITE_TEST_POSTGRES_DSN must identify a disposable test database.
func TestPostgresRegionalOfficialProgress(t *testing.T) {
	dsn := os.Getenv("SATELLITE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("a disposable PostgreSQL instance is required")
	}
	t.Setenv("SATELLITED_POSTGRES_DSN", dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	open := func() *Store {
		t.Helper()
		s, err := Open(filepath.Join(t.TempDir(), "unused.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if !s.postgres {
			t.Fatal("expected PostgreSQL")
		}
		return s
	}
	s := open()
	defer func() { _ = s.Close() }()
	const key = "himawari9:AHI-L1b-Japan"
	day := time.Date(2088, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := s.UpsertProducts(ctx, "himawari9", []model.Product{{ProductKey: key, SourceID: "himawari9"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueRange(ctx, key, day, day.AddDate(0, 0, 1), 10); err != nil {
		t.Fatal(err)
	}
	type result struct {
		batch WorkBatch
		err   error
	}
	claims := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; b, e := s.ClaimBatch(ctx, 7); claims <- result{b, e} }()
	}
	close(start)
	wg.Wait()
	close(claims)
	var claim WorkBatch
	n := 0
	for r := range claims {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if len(r.batch.Days) > 0 {
			n++
			claim = r.batch
		}
	}
	if n != 1 || len(claim.Days) != 1 {
		t.Fatalf("concurrent regional claims=%d", n)
	}
	from, _, err := s.RegionalOfficialWindow(ctx, claim)
	if err != nil || !from.Equal(day) {
		t.Fatalf("first %s %v", from, err)
	}
	granule := model.Granule{ID: "pg-scene", Start: day.Add(time.Minute), Assets: []model.Asset{{Name: "pg-scene.hsd", URL: "https://example.test/pg-scene.hsd"}}}
	if done, err := s.SaveRegionalOfficialWindow(ctx, claim, from, []model.Granule{granule}); err != nil || done {
		t.Fatalf("save %v %v", done, err)
	}
	claim, err = s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	from, _, err = s.RegionalOfficialWindow(ctx, claim)
	if err != nil || !from.Equal(day.Add(10*time.Minute)) {
		t.Fatalf("cursor %s %v", from, err)
	}
	stale := claim
	if err := s.MarkBatchError(ctx, claim, errors.New("simulated")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `UPDATE official_work SET next_attempt_ns=0 WHERE product_key=? AND day=?`, key, day.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRegionalOfficialWindow(ctx, stale, from, nil); err == nil {
		t.Fatal("stale claim accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	if err := s.RecoverOfficialWork(ctx); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	from, _, err = s.RegionalOfficialWindow(ctx, claim)
	if err != nil || !from.Equal(day.Add(10*time.Minute)) {
		t.Fatalf("recovered cursor %s %v", from, err)
	}
	for idx := 1; idx < 144; idx++ {
		from, _, err = s.RegionalOfficialWindow(ctx, claim)
		if err != nil || !from.Equal(day.Add(time.Duration(idx)*10*time.Minute)) {
			t.Fatalf("window %d %s %v", idx, from, err)
		}
		done, err := s.SaveRegionalOfficialWindow(ctx, claim, from, nil)
		if err != nil || done != (idx == 143) {
			t.Fatalf("save %d done=%v err=%v", idx, done, err)
		}
		if !done {
			claim, err = s.ClaimBatch(ctx, 7)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var days, staged, work int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_days WHERE product_key=? AND day=? AND granule_count=1`, key, day.Format(time.DateOnly)).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM regional_official_progress WHERE product_key=? AND day=?`, key, day.Format(time.DateOnly)).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_work WHERE product_key=? AND day=?`, key, day.Format(time.DateOnly)).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if days != 1 || staged != 0 || work != 0 {
		t.Fatalf("publication day=%d staging=%d work=%d", days, staged, work)
	}
	t.Run("nonregional-neighbour-lock", func(t *testing.T) { testPostgresNeighbourClaim(t, ctx, s) })
}

func testPostgresNeighbourClaim(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	const key = "goes19:pg-claim-regression"
	day := time.Date(2088, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := s.UpsertProducts(ctx, "goes19", []model.Product{{ProductKey: key, SourceID: "goes19"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueRange(ctx, key, day, day.AddDate(0, 0, 3), 10); err != nil {
		t.Fatal(err)
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var locked int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM official_work WHERE product_key=? AND day=? FOR UPDATE`, key, day.AddDate(0, 0, 1).Format(time.DateOnly)).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Days) != 1 || first.Days[0] != day.AddDate(0, 0, 2).Format(time.DateOnly) {
		t.Fatalf("claimed locked neighbour: %+v", first)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimBatch(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Days) != 2 || second.Days[0] != day.Format(time.DateOnly) || second.Days[1] != day.AddDate(0, 0, 1).Format(time.DateOnly) {
		t.Fatalf("remaining: %+v", second)
	}
	seen := map[string]bool{}
	for _, b := range []WorkBatch{first, second} {
		for _, d := range b.Days {
			if seen[d] {
				t.Fatalf("duplicate day %s", d)
			}
			seen[d] = true
		}
	}
}
