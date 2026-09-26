package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"modernc.org/sqlite"
)

func TestSQLitePoolBusyTimeout(t *testing.T) {
	for _, uri := range []bool{false, true} {
		t.Run(fmt.Sprintf("uri=%t", uri), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			if uri {
				path = "file:" + path + "?cache=private"
			}
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var held []*sql.Conn
			defer func() {
				for _, conn := range held {
					_ = conn.Close()
				}
			}()
			for i := 0; i < 8; i++ {
				conn, err := store.db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				held = append(held, conn)
				var timeout int
				if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
					t.Fatal(err)
				}
				if timeout != 30000 {
					t.Fatalf("connection %d busy_timeout=%d", i, timeout)
				}
			}
		})
	}
}

func TestWriteTransactionsAcquireLockBeforeReads(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	contender, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM official_work").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := contender.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
		_, _ = contender.ExecContext(ctx, "ROLLBACK")
		t.Fatal("write transaction did not reserve the SQLite writer lock before reading")
	} else {
		var busy *sqlite.Error
		if !errors.As(err, &busy) || busy.Code() != 5 {
			t.Fatalf("expected SQLITE_BUSY for competing writer, got %v", err)
		}
	}
}

func TestConcurrentCalendarEnqueueAndClaim(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	product := model.Product{ProductKey: "goes18:ABI-L1b-RadF", SourceID: "goes18", Cadence: "10m"}
	if err := store.UpsertProducts(ctx, product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2023, 5, 1, 0, 0, 0, 0, time.UTC)
	const days = 48
	claimed := make(chan string, days*2)
	failures := make(chan error, 8)
	var workers sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		workers.Add(1)
		go func(writer int) {
			defer workers.Done()
			for offset := writer; offset < days; offset += 4 {
				day := start.AddDate(0, 0, offset)
				if err := store.EnqueueRange(ctx, product.ProductKey, day, day.AddDate(0, 0, 1), 100); err != nil {
					failures <- fmt.Errorf("enqueue writer %d: %w", writer, err)
					return
				}
			}
		}(writer)
	}
	for worker := 0; worker < 2; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < days; i++ {
				batch, err := store.ClaimBatch(ctx, 2)
				if err != nil {
					failures <- fmt.Errorf("claim worker %d: %w", worker, err)
					return
				}
				for _, day := range batch.Days {
					claimed <- day
				}
				if _, err := store.Day(ctx, product.ProductKey, start.Format(time.DateOnly)); err != nil {
					failures <- fmt.Errorf("calendar reader %d: %w", worker, err)
					return
				}
			}
		}(worker)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	for {
		batch, err := store.ClaimBatch(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Days) == 0 {
			break
		}
		for _, day := range batch.Days {
			claimed <- day
		}
	}
	close(claimed)
	seen := make(map[string]bool, days)
	for day := range claimed {
		if seen[day] {
			t.Errorf("day %s claimed more than once", day)
		}
		seen[day] = true
	}
	if len(seen) != days {
		t.Fatalf("claimed %d distinct days, expected %d", len(seen), days)
	}
}
