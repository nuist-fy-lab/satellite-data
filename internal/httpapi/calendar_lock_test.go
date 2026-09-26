package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
