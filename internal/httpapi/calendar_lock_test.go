package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

func TestCalendarDayWhileIndexerClaimsWork(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
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
	handler := New(nil, store)
	dates := []string{"2023-05-26", "2023-05-27", "2023-07-01"}
	start := make(chan struct{})
	failures := make(chan error, 8)
	var workers sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			for i := 0; i < 96; i++ {
				if _, err := store.ClaimBatch(ctx, 2); err != nil {
					failures <- fmt.Errorf("indexer %d: %w", worker, err)
					return
				}
			}
		}(worker)
	}
	for reader := 0; reader < 6; reader++ {
		workers.Add(1)
		go func(reader int) {
			defer workers.Done()
			<-start
			for i := 0; i < 18; i++ {
				query := url.Values{"product": {product.ProductKey}, "day": {dates[(reader+i)%len(dates)]}}
				request := httptest.NewRequest(http.MethodGet, "/api/day?"+query.Encode(), nil).WithContext(ctx)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					failures <- fmt.Errorf("calendar reader %d: status=%d body=%s", reader, response.Code, response.Body.String())
					return
				}
			}
		}(reader)
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestIndexedHistoricalCalendarDayReturnsWithoutReindexing(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	product := model.Product{ProductKey: "modis:C1443536017-LAADS", SourceID: "modis", Cadence: "5m"}
	if err := store.UpsertProducts(ctx, product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	day := "2023-05-03"
	if err := store.SaveOfficialBatch(ctx, state.WorkBatch{Product: product.ProductKey, Days: []string{day}}, nil); err != nil {
		t.Fatal(err)
	}
	handler := New(nil, store)
	request := httptest.NewRequest(http.MethodGet, "/api/day?product=modis%3AC1443536017-LAADS&day="+day, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("day returned %d: %s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); !strings.Contains(got, `"day":"2023-05-03"`) {
		t.Fatalf("unexpected response: %s", got)
	}
	if batch, err := store.ClaimBatch(ctx, 1); err != nil || len(batch.Days) != 0 {
		t.Fatalf("fresh day should not re-enqueue: batch=%v err=%v", batch, err)
	}
}

func TestUnindexedCalendarDayIsReadableDuringSQLiteWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	product := model.Product{ProductKey: "modis:C1443536017-LAADS", SourceID: "modis", Cadence: "5m"}
	if err := store.UpsertProducts(ctx, product.SourceID, []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite", path+"?_txlock=immediate&_busy_timeout=1000")
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	conn, err := locker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	request := httptest.NewRequest(http.MethodGet, "/api/day?product=modis%3AC1443536017-LAADS&day=2023-05-03", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	started := time.Now()
	New(nil, store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("read-only day should not block on writer: status=%d body=%s", response.Code, response.Body.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("read-only day waited on SQLite writer for %s", elapsed)
	}
}
