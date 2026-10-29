package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloadfilter"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestPostgresCompactStateRoundTrip(t *testing.T) {
	dsn := os.Getenv("SATELLITE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("a disposable PostgreSQL instance is required")
	}
	t.Setenv("SATELLITED_POSTGRES_DSN", dsn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := Open(filepath.Join(t.TempDir(), "unused.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.postgres {
		t.Fatal("expected PostgreSQL, not SQLite")
	}
	product := model.Product{ProductKey: "modis:Ctest", SourceID: "modis", DataType: "MYD021KM", Cadence: "5m"}
	if err := s.UpsertProducts(ctx, "modis", []model.Product{product}); err != nil {
		t.Fatal(err)
	}
	products, err := s.Products(ctx, "modis")
	if err != nil || len(products) != 1 || products[0].LocalCovered {
		t.Fatalf("PostgreSQL product without local coverage: %+v, %v", products, err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO local_slots
		(product_key,slot_ts,path,filename,size,mtime_ns,state,detail,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, product.ProductKey, "2006-07-23T21:45:00Z",
		"/tmp/pg-coverage-one.hdf", "one.hdf", 4, int64(1), "ok", "", "2026-10-20T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	products, err = s.Products(ctx, "modis")
	if err != nil || len(products) != 1 || !products[0].LocalCovered {
		t.Fatalf("PostgreSQL product with local coverage: %+v, %v", products, err)
	}
	if products[0].FirstDay == nil || *products[0].FirstDay != "2006-07-23" ||
		products[0].LastDay == nil || *products[0].LastDay != "2006-07-23" {
		t.Fatalf("PostgreSQL product local date bounds: %+v", products[0])
	}
	start := time.Date(2006, 7, 23, 0, 0, 0, 0, time.UTC)
	assets := []model.Asset{
		{ID: "1", Name: "one.hdf", URL: "https://test.example.org/a/b/one.hdf?name=1", Size: 1099511627776},
		{ID: "2", Name: "two.hdf", URL: "https://test.example.org/a/b/two.hdf", Size: 4},
		{ID: "3", Name: "three.hdf", URL: "not-a-url", Size: 0},
	}
	granule := model.Granule{ID: "g1", SourceID: "modis", CollectionID: product.ProductKey,
		Start: start.Add(21*time.Hour + 45*time.Minute), Assets: assets}
	if err := s.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey,
		Days: []string{"2006-07-23"}}, []model.Granule{granule}); err != nil {
		t.Fatal(err)
	}
	// A provider page may repeat the same granule. It must not abort the
	// entire PostgreSQL index refresh or duplicate its assets.
	if err := s.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey,
		Days: []string{"2006-07-23"}}, []model.Granule{granule, granule}); err != nil {
		t.Fatalf("duplicate granule in one response: %v", err)
	}
	var granuleCount, assetCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_official_granule_keys
		WHERE product_key=?`, product.ProductKey).Scan(&granuleCount); err != nil || granuleCount != 1 {
		t.Fatalf("duplicate granule was not consolidated: %d %v", granuleCount, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_assets
		WHERE product_key=?`, product.ProductKey).Scan(&assetCount); err != nil || assetCount != len(assets) {
		t.Fatalf("duplicate assets were not consolidated: %d %v", assetCount, err)
	}
	if _, err := s.ReplaceLocalFilesWithWarnings(ctx,
		[]LocalFile{{Path: "/tmp/pg-unrelated.hdf", Filename: "unrelated.hdf", Size: 5 << 30, MtimeNS: 1790610031212360399, State: "ok"}},
		"postgres-local-scan-regression", nil); err != nil {
		t.Fatalf("PostgreSQL local archive scan: %v", err)
	}
	for _, asset := range assets {
		var url string
		var size int64
		if err := s.db.QueryRowContext(ctx, `SELECT url,size FROM official_assets WHERE filename=?`,
			asset.Name).Scan(&url, &size); err != nil {
			t.Fatal(err)
		}
		if url != asset.URL || size != asset.Size {
			t.Fatalf("asset was altered during normalization: %q %d", url, size)
		}
	}
	var storedSuffix sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT url_suffix FROM pg_official_asset_details WHERE filename=?`,
		"two.hdf").Scan(&storedSuffix); err != nil {
		t.Fatal(err)
	}
	if storedSuffix.Valid {
		t.Fatalf("canonical URL suffix was redundantly stored: %q", storedSuffix.String)
	}
	group, created, err := s.EnqueueDownloadGroup(ctx, "researcher", start, start.AddDate(1, 0, 0),
		[]GroupProduct{{Source: "modis", Provider: "modis", Collection: product.ProductKey,
			Selection: downloadfilter.Selection{Scenes: map[string][]string{"2006-07-23": {"21:45"}}}}})
	if err != nil || !created || group.Products != 1 {
		t.Fatalf("first group %+v created=%v err=%v", group, created, err)
	}
	group2, created, err := s.EnqueueDownloadGroup(ctx, "researcher", start, start.AddDate(1, 0, 0),
		[]GroupProduct{{Source: "modis", Provider: "modis", Collection: product.ProductKey,
			Selection: downloadfilter.Selection{Scenes: map[string][]string{"2006-07-23": {"21:45"}}}}})
	if err != nil || created || group2.ID != group.ID {
		t.Fatalf("idempotent group %+v created=%v err=%v", group2, created, err)
	}
	if err := s.SaveOfficialBatch(ctx, WorkBatch{Product: product.ProductKey,
		Days: []string{"2006-07-23"}}, []model.Granule{granule}); err != nil {
		t.Fatal(err)
	}
	var indexed int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_assets WHERE product_key=?`,
		product.ProductKey).Scan(&indexed); err != nil || indexed != len(assets) {
		t.Fatalf("repeated index refresh lost or duplicated assets: %d %v", indexed, err)
	}
}
