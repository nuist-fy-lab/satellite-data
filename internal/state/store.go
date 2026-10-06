package state

import (
	"container/list"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/slotaxis"
	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS products (
  product_key TEXT PRIMARY KEY,
  source_id TEXT NOT NULL,
  official_id TEXT NOT NULL DEFAULT '',
  satellite TEXT NOT NULL DEFAULT '',
  instrument TEXT NOT NULL DEFAULT '',
  level TEXT NOT NULL DEFAULT '',
  data_type TEXT NOT NULL DEFAULT '',
  area_type TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  resolution TEXT NOT NULL DEFAULT '',
  cadence TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  access_tier TEXT NOT NULL DEFAULT '',
  catalog_source TEXT NOT NULL DEFAULT '',
  product_count INTEGER NOT NULL DEFAULT 0,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS products_source_idx ON products(source_id);

CREATE TABLE IF NOT EXISTS catalog_sources (
  source_id TEXT PRIMARY KEY,
  state TEXT NOT NULL DEFAULT 'unknown',
  refreshed_at TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS official_slots (
  product_key TEXT NOT NULL,
  slot_ts TEXT NOT NULL,
  granule_id TEXT NOT NULL,
  asset_count INTEGER NOT NULL DEFAULT 0,
  total_size INTEGER NOT NULL DEFAULT 0,
  representative_filename TEXT NOT NULL DEFAULT '',
  refreshed_at TEXT NOT NULL,
  PRIMARY KEY(product_key,slot_ts,granule_id)
);
CREATE INDEX IF NOT EXISTS official_slots_day_idx ON official_slots(product_key,substr(slot_ts,1,10));

CREATE TABLE IF NOT EXISTS official_assets (
  product_key TEXT NOT NULL,
  slot_ts TEXT NOT NULL,
  granule_id TEXT NOT NULL,
  filename TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  etag TEXT NOT NULL DEFAULT '',
  media_type TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(product_key,slot_ts,granule_id,filename)
);
CREATE INDEX IF NOT EXISTS official_assets_filename_idx ON official_assets(filename);
CREATE INDEX IF NOT EXISTS official_assets_day_idx ON official_assets(product_key,substr(slot_ts,1,10));

CREATE TABLE IF NOT EXISTS official_days (
  product_key TEXT NOT NULL,
  day TEXT NOT NULL,
  state TEXT NOT NULL,
  granule_count INTEGER NOT NULL DEFAULT 0,
  asset_count INTEGER NOT NULL DEFAULT 0,
  refreshed_at TEXT NOT NULL,
  PRIMARY KEY(product_key,day)
);

CREATE TABLE IF NOT EXISTS archive_day_cursor (
  product_key TEXT NOT NULL,
  day TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(product_key,day)
);

CREATE TABLE IF NOT EXISTS local_files (
  path TEXT PRIMARY KEY,
  filename TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  state TEXT NOT NULL DEFAULT 'ok',
  detail TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS local_files_filename_idx ON local_files(filename);

CREATE TABLE IF NOT EXISTS local_slots (
  product_key TEXT NOT NULL,
  slot_ts TEXT NOT NULL,
  path TEXT NOT NULL,
  filename TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  state TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(product_key,slot_ts,path)
);
CREATE INDEX IF NOT EXISTS local_slots_day_idx ON local_slots(product_key,substr(slot_ts,1,10));
CREATE INDEX IF NOT EXISTS local_slots_path_idx ON local_slots(path);

CREATE TABLE IF NOT EXISTS day_summary (
  product_key TEXT NOT NULL,
  day TEXT NOT NULL,
  status TEXT NOT NULL,
  official_count INTEGER NOT NULL DEFAULT 0,
  local_ok_count INTEGER NOT NULL DEFAULT 0,
  missing_count INTEGER NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0,
  processing_count INTEGER NOT NULL DEFAULT 0,
  hourly_official_count INTEGER NOT NULL DEFAULT 0,
  hourly_local_ok_count INTEGER NOT NULL DEFAULT 0,
  official_gap_count INTEGER NOT NULL DEFAULT 0,
  hourly_official_gap_count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(product_key,day)
);
CREATE INDEX IF NOT EXISTS day_summary_year_idx ON day_summary(product_key,day);

CREATE TABLE IF NOT EXISTS official_work (
  product_key TEXT NOT NULL,
  day TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  priority INTEGER NOT NULL DEFAULT 0,
  requested_at TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL DEFAULT '',
  failure_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_ns INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(product_key,day)
);
CREATE INDEX IF NOT EXISTS official_work_ready_idx
ON official_work(state,priority DESC,day,product_key,next_attempt_ns);

CREATE TABLE IF NOT EXISTS local_scan_state (
  id INTEGER PRIMARY KEY CHECK(id=1),
  fingerprint TEXT NOT NULL DEFAULT '',
  scanned_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS verified_archive_files (
  path TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  source_id TEXT NOT NULL,
  official_collection TEXT NOT NULL,
  official_item TEXT NOT NULL,
  official_asset TEXT NOT NULL,
  verified_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS archive_scan_status (
  id INTEGER PRIMARY KEY CHECK(id=1),
  skipped_paths_json TEXT NOT NULL DEFAULT '[]',
  scanned_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS link_samples (
  source_id TEXT NOT NULL,
  sampled_at_ns INTEGER NOT NULL,
  target_host TEXT NOT NULL,
  resolved_ip TEXT NOT NULL DEFAULT '',
  latency_ms REAL,
  jitter_ms REAL,
  loss_pct REAL NOT NULL,
  success_count INTEGER NOT NULL,
  attempt_count INTEGER NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(source_id,sampled_at_ns)
);
CREATE INDEX IF NOT EXISTS link_samples_time_idx ON link_samples(sampled_at_ns);
CREATE INDEX IF NOT EXISTS link_samples_source_time_idx ON link_samples(source_id,sampled_at_ns);

CREATE TABLE IF NOT EXISTS download_monitor_samples (
  provider_id TEXT NOT NULL,
  sampled_at_ns INTEGER NOT NULL,
  bytes_per_second REAL NOT NULL,
  active_transfers INTEGER NOT NULL,
  adaptive_concurrency INTEGER NOT NULL,
  queued INTEGER NOT NULL,
  verifying INTEGER NOT NULL,
  verify_wait INTEGER NOT NULL DEFAULT 0,
  promoting INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(provider_id,sampled_at_ns)
);
CREATE INDEX IF NOT EXISTS download_monitor_samples_time_idx ON download_monitor_samples(sampled_at_ns);
`

type Store struct {
	db            *sql.DB
	writer        *sql.DB
	coverageMu    sync.Mutex
	coverageCache map[coverageKey]*list.Element
	coverageLRU   list.List
	scanMu        sync.RWMutex
	scanRuntime   archiveScanRuntime
}

type YearDay struct {
	Day                    string  `json:"day"`
	Status                 string  `json:"status"`
	OfficialCount          int     `json:"official_count"`
	LocalOKCount           int     `json:"local_ok_count"`
	MissingCount           int     `json:"missing_count"`
	ErrorCount             int     `json:"error_count"`
	ProcessingCount        int     `json:"processing_count"`
	HourlyOfficialCount    int     `json:"hourly_official_count"`
	HourlyLocalOKCount     int     `json:"hourly_local_ok_count"`
	OfficialGapCount       int     `json:"official_gap_count"`
	HourlyOfficialGapCount int     `json:"hourly_official_gap_count"`
	UpdatedAt              *string `json:"updated_at"`
	OfficialFileCount      *int    `json:"official_file_count"`
	Indexing               int     `json:"indexing"`
	Recomputing            int     `json:"recomputing"`
}

type YearResponse struct {
	Product string    `json:"product"`
	Year    int       `json:"year"`
	Days    []YearDay `json:"days"`
}

type OfficialDay struct {
	State        string `json:"state"`
	GranuleCount int    `json:"granule_count"`
	AssetCount   int    `json:"asset_count"`
	RefreshedAt  string `json:"refreshed_at"`
}

type OfficialSlot struct {
	SlotTS                 string `json:"slot_ts"`
	GranuleID              string `json:"granule_id"`
	AssetCount             int    `json:"asset_count"`
	TotalSize              int64  `json:"total_size"`
	RepresentativeFilename string `json:"filename"`
}

type LocalSlot struct {
	SlotTS   string `json:"slot_ts"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	Path     string `json:"path"`
}

type DaySlot struct {
	Slot        string         `json:"slot"`
	NominalSlot string         `json:"nominal_slot,omitempty"`
	Status      string         `json:"status"`
	Official    []OfficialSlot `json:"official"`
	Local       []LocalSlot    `json:"local"`
}

type DayResponse struct {
	Product               string       `json:"product"`
	Day                   string       `json:"day"`
	AreaType              string       `json:"area_type"`
	MatchToleranceSeconds int          `json:"match_tolerance_seconds"`
	CadenceMinutes        int          `json:"cadence_minutes"`
	TimelineMode          string       `json:"timeline_mode"`
	OfficialGapCount      int          `json:"official_gap_count"`
	OfficialDay           *OfficialDay `json:"official_day"`
	SummaryBasis          string       `json:"summary_basis"`
	Indexing              int          `json:"indexing"`
	Slots                 []DaySlot    `json:"slots"`
}

type WorkBatch struct {
	Product string
	Days    []string
}

type LocalFile struct {
	Path     string
	Filename string
	Size     int64
	MtimeNS  int64
	State    string
	Detail   string
}

type VerifiedArchiveFile struct {
	Size    int64
	MtimeNS int64
	SHA256  string
}

type ArchiveScanStatus struct {
	State        string   `json:"state"`
	StartedAt    string   `json:"started_at,omitempty"`
	Observed     int      `json:"observed_files"`
	LastError    string   `json:"last_error,omitempty"`
	SkippedPaths []string `json:"skipped_paths"`
	ScannedAt    string   `json:"scanned_at"`
	Files        int      `json:"files"`
	Verified     int      `json:"verified"`
	Unverified   int      `json:"unverified"`
	Errors       int      `json:"errors"`
}

type ArchiveProductDay struct {
	ProductType string
	Day         string
}

func (s *Store) QueueEarthCAREOfficialDays(ctx context.Context, days []ArchiveProductDay) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	queued := 0
	for _, day := range days {
		if _, err := time.Parse(time.DateOnly, day.Day); err != nil {
			return queued, err
		}
		result, err := s.writer.ExecContext(ctx, `
			INSERT OR IGNORE INTO official_work(product_key,day,state,priority,requested_at,updated_at)
			SELECT p.product_key,?,'pending',-5,?,? FROM products p
			WHERE p.source_id='earthcare' AND p.data_type=?
			AND NOT EXISTS (SELECT 1 FROM official_days d WHERE d.product_key=p.product_key AND d.day=?)`,
			day.Day, now, now, day.ProductType, day.Day)
		if err != nil {
			return queued, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return queued, err
		}
		queued += int(n)
	}
	return queued, nil
}

type LinkSample struct {
	SourceID     string    `json:"source_id"`
	SampledAt    time.Time `json:"sampled_at"`
	TargetHost   string    `json:"target_host"`
	ResolvedIP   string    `json:"resolved_ip,omitempty"`
	LatencyMS    *float64  `json:"latency_ms,omitempty"`
	JitterMS     *float64  `json:"jitter_ms,omitempty"`
	LossPct      float64   `json:"loss_pct"`
	SuccessCount int       `json:"success_count"`
	AttemptCount int       `json:"attempt_count"`
	LastError    string    `json:"last_error,omitempty"`
}

type LinkPoint struct {
	SampledAt time.Time `json:"sampled_at"`
	LatencyMS *float64  `json:"latency_ms,omitempty"`
	JitterMS  *float64  `json:"jitter_ms,omitempty"`
	LossPct   float64   `json:"loss_pct"`
}

func Open(path string) (*Store, error) {
	// busy_timeout is connection-local in SQLite. Put it in the DSN so every
	// pooled connection receives the same wait policy; executing PRAGMA once on
	// *sql.DB only configures whichever connection happens to service that call.
	// Reserve the writer lock before reading in read-modify-write transactions;
	// a deferred transaction cannot wait safely when upgrading a stale snapshot.
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dsn := path + separator + "_busy_timeout=30000&_txlock=immediate&_foreign_keys=on"
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Queue centre writes instead of making workers compete for SQLite's single
	// writer lock. Readers retain their own pool and can run during WAL writes.
	writer.SetMaxOpenConns(1)
	if _, err := writer.Exec(schema); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("initialize state schema: %w", err)
	}
	if err := initDownloadSchema(writer); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("initialize download schema: %w", err)
	}
	if err := initDownloadMonitorSchema(writer); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("initialize download monitor schema: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	db.SetMaxOpenConns(8)
	return &Store{db: db, writer: writer}, nil
}

func (s *Store) Close() error { return errors.Join(s.db.Close(), s.writer.Close()) }

func (s *Store) RecordLinkSample(ctx context.Context, sample LinkSample) error {
	var latency any
	if sample.LatencyMS != nil {
		latency = *sample.LatencyMS
	}
	var jitter any
	if sample.JitterMS != nil {
		jitter = *sample.JitterMS
	}
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO link_samples(
			source_id,sampled_at_ns,target_host,resolved_ip,latency_ms,jitter_ms,loss_pct,
			success_count,attempt_count,last_error)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		sample.SourceID, sample.SampledAt.UTC().UnixNano(), sample.TargetHost, sample.ResolvedIP,
		latency, jitter, sample.LossPct, sample.SuccessCount, sample.AttemptCount, sample.LastError)
	return err
}

func (s *Store) LatestLinkSamples(ctx context.Context) ([]LinkSample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.source_id,l.sampled_at_ns,l.target_host,l.resolved_ip,l.latency_ms,l.jitter_ms,l.loss_pct,
		       l.success_count,l.attempt_count,l.last_error
		FROM link_samples l
		JOIN (
			SELECT source_id,max(sampled_at_ns) sampled_at_ns
			FROM link_samples
			GROUP BY source_id
		) latest
		  ON latest.source_id=l.source_id AND latest.sampled_at_ns=l.sampled_at_ns
		ORDER BY l.source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LinkSample, 0, 16)
	for rows.Next() {
		var item LinkSample
		var sampledNS int64
		var latency, jitter sql.NullFloat64
		if err := rows.Scan(&item.SourceID, &sampledNS, &item.TargetHost, &item.ResolvedIP, &latency, &jitter,
			&item.LossPct, &item.SuccessCount, &item.AttemptCount, &item.LastError); err != nil {
			return nil, err
		}
		item.SampledAt = time.Unix(0, sampledNS).UTC()
		if latency.Valid {
			value := latency.Float64
			item.LatencyMS = &value
		}
		if jitter.Valid {
			value := jitter.Float64
			item.JitterMS = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) LinkSeries(ctx context.Context, since time.Time, bucket time.Duration) (map[string][]LinkPoint, error) {
	if bucket <= 0 {
		bucket = 15 * time.Second
	}
	bucketNS := bucket.Nanoseconds()
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_id,(sampled_at_ns / ?) * ? bucket_ns,
		       avg(latency_ms),avg(jitter_ms),avg(loss_pct)
		FROM link_samples
		WHERE sampled_at_ns>=?
		GROUP BY source_id,bucket_ns
		ORDER BY source_id,bucket_ns`,
		bucketNS, bucketNS, since.UTC().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]LinkPoint{}
	for rows.Next() {
		var sourceID string
		var sampledNS int64
		var latency, jitter sql.NullFloat64
		var loss float64
		if err := rows.Scan(&sourceID, &sampledNS, &latency, &jitter, &loss); err != nil {
			return nil, err
		}
		item := LinkPoint{SampledAt: time.Unix(0, sampledNS).UTC(), LossPct: loss}
		if latency.Valid {
			value := latency.Float64
			item.LatencyMS = &value
		}
		if jitter.Valid {
			value := jitter.Float64
			item.JitterMS = &value
		}
		out[sourceID] = append(out[sourceID], item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteLinkSamplesBefore(ctx context.Context, before time.Time) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM link_samples WHERE sampled_at_ns<?`, before.UTC().UnixNano())
	return err
}

func (s *Store) RecoverOfficialWork(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.writer.ExecContext(ctx, `
		UPDATE official_work
		SET state='pending',started_at='',next_attempt_ns=0,updated_at=?
		WHERE state='running'`, now)
	return err
}

func (s *Store) ProductCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM products`).Scan(&count)
	return count, err
}

func (s *Store) UpsertProducts(ctx context.Context, sourceID string, products []model.Product) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seen := make(map[string]struct{}, len(products))
	for _, product := range products {
		seen[product.ProductKey] = struct{}{}
		metadata, _ := json.Marshal(product.Metadata)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO products(product_key,source_id,official_id,satellite,instrument,level,data_type,area_type,display_name,
			 resolution,cadence,version,access_tier,catalog_source,product_count,metadata_json,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(product_key) DO UPDATE SET
			 source_id=excluded.source_id,official_id=excluded.official_id,satellite=excluded.satellite,
			 instrument=excluded.instrument,level=excluded.level,data_type=excluded.data_type,area_type=excluded.area_type,
			 display_name=excluded.display_name,resolution=excluded.resolution,cadence=excluded.cadence,
			 version=excluded.version,access_tier=excluded.access_tier,catalog_source=excluded.catalog_source,
			 product_count=excluded.product_count,metadata_json=excluded.metadata_json,updated_at=excluded.updated_at`,
			product.ProductKey, product.SourceID, product.OfficialID, product.Satellite, product.Instrument, product.Level,
			product.DataType, product.AreaType, product.DisplayName, product.Resolution, product.Cadence, product.Version,
			string(product.AccessTier), product.CatalogSource, product.ProductCount, string(metadata), now)
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT product_key FROM products WHERE source_id=?`, sourceID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		if _, ok := seen[key]; !ok {
			stale = append(stale, key)
		}
	}
	rows.Close()
	for _, key := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM products WHERE product_key=?`, key); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO catalog_sources(source_id,state,refreshed_at,last_error,updated_at) VALUES(?,'ready',?,'',?)
		ON CONFLICT(source_id) DO UPDATE SET state='ready',refreshed_at=excluded.refreshed_at,last_error='',updated_at=excluded.updated_at`,
		sourceID, now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CatalogError(ctx context.Context, sourceID string, cause error) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO catalog_sources(source_id,state,last_error,updated_at) VALUES(?,'error',?,?)
		ON CONFLICT(source_id) DO UPDATE SET state='error',last_error=excluded.last_error,updated_at=excluded.updated_at`,
		sourceID, cause.Error(), now)
	return err
}

func (s *Store) Products(ctx context.Context, sourceID string) ([]model.Product, error) {
	query := `
		SELECT p.product_key,p.source_id,p.official_id,p.satellite,p.instrument,p.level,p.data_type,p.area_type,p.display_name,
		       p.resolution,p.cadence,p.version,p.access_tier,p.catalog_source,p.product_count,p.metadata_json,
		       EXISTS(SELECT 1 FROM local_slots l WHERE l.product_key=p.product_key AND l.state='ok' LIMIT 1),
		       (SELECT min(substr(slot_ts,1,10)) FROM local_slots l WHERE l.product_key=p.product_key AND l.state='ok'),
		       (SELECT max(substr(slot_ts,1,10)) FROM local_slots l WHERE l.product_key=p.product_key AND l.state='ok')
		FROM products p`
	args := []any{}
	if sourceID != "" {
		query += " WHERE p.source_id=?"
		args = append(args, sourceID)
	}
	query += " ORDER BY p.source_id,p.satellite,p.instrument,p.level,p.data_type,p.version"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Product, 0, 512)
	for rows.Next() {
		var item model.Product
		var tier, metadata string
		var localCovered int
		var first, last sql.NullString
		if err := rows.Scan(&item.ProductKey, &item.SourceID, &item.OfficialID, &item.Satellite, &item.Instrument,
			&item.Level, &item.DataType, &item.AreaType, &item.DisplayName, &item.Resolution, &item.Cadence,
			&item.Version, &tier, &item.CatalogSource, &item.ProductCount, &metadata, &localCovered, &first, &last); err != nil {
			return nil, err
		}
		item.AccessTier = model.AccessTier(tier)
		item.LocalCovered = localCovered != 0
		_ = json.Unmarshal([]byte(metadata), &item.Metadata)
		if first.Valid {
			value := first.String
			item.FirstDay = &value
			if len(value) >= 4 {
				year := value[:4]
				item.FirstYear = &year
			}
		}
		if last.Valid {
			value := last.String
			item.LastDay = &value
			if len(value) >= 4 {
				year := value[:4]
				item.LastYear = &year
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ProductSource(ctx context.Context, product string) (string, model.Product, error) {
	products, err := s.Products(ctx, "")
	if err != nil {
		return "", model.Product{}, err
	}
	for _, item := range products {
		if item.ProductKey == product {
			return item.SourceID, item, nil
		}
	}
	return "", model.Product{}, sql.ErrNoRows
}

func (s *Store) EnqueueRange(ctx context.Context, product string, start, end time.Time, priority int) error {
	if !end.After(start) {
		return errors.New("end must be after start")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for day := dayFloor(start); day.Before(end); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		var refreshed string
		err := tx.QueryRowContext(ctx, `SELECT refreshed_at FROM official_days WHERE product_key=? AND day=?`, product, key).Scan(&refreshed)
		if err == nil {
			if stamp, parseErr := time.Parse(time.RFC3339Nano, refreshed); parseErr == nil && time.Since(stamp) < 24*time.Hour {
				continue
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO official_work(product_key,day,state,priority,requested_at,updated_at)
			VALUES(?,?,'pending',?,?,?)
			ON CONFLICT(product_key,day) DO UPDATE SET
			  priority=max(official_work.priority,excluded.priority),
			  state=CASE WHEN official_work.state='running' THEN official_work.state ELSE 'pending' END,
			  requested_at=excluded.requested_at,updated_at=excluded.updated_at`,
			product, key, priority, now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ClaimBatch(ctx context.Context, maxDays int) (WorkBatch, error) {
	if maxDays < 1 {
		maxDays = 1
	}
	nowNS := time.Now().UnixNano()
	var pending bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM official_work WHERE state='pending' AND next_attempt_ns<=?)`, nowNS).Scan(&pending); err != nil {
		return WorkBatch{}, err
	}
	if !pending {
		return WorkBatch{}, nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return WorkBatch{}, err
	}
	defer tx.Rollback()
	var product, firstDay string
	err = tx.QueryRowContext(ctx, `
		SELECT product_key,day FROM official_work
		WHERE state='pending' AND next_attempt_ns<=?
		ORDER BY priority DESC,requested_at DESC,day DESC LIMIT 1`, nowNS).Scan(&product, &firstDay)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkBatch{}, nil
	}
	if err != nil {
		return WorkBatch{}, err
	}
	first, err := time.Parse(time.DateOnly, firstDay)
	if err != nil {
		return WorkBatch{}, err
	}
	days := make([]string, 0, maxDays)
	for offset := 0; offset < maxDays; offset++ {
		day := first.AddDate(0, 0, -offset).Format(time.DateOnly)
		var exists int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM official_work
			WHERE product_key=? AND day=? AND state='pending' AND next_attempt_ns<=?`, product, day, nowNS).Scan(&exists); err != nil {
			return WorkBatch{}, err
		}
		if exists == 0 {
			break
		}
		days = append(days, day)
	}
	sort.Strings(days)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, day := range days {
		if _, err := tx.ExecContext(ctx, `
			UPDATE official_work SET state='running',started_at=?,updated_at=? WHERE product_key=? AND day=?`,
			now, now, product, day); err != nil {
			return WorkBatch{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WorkBatch{}, err
	}
	return WorkBatch{Product: product, Days: days}, nil
}

func (s *Store) MarkBatchError(ctx context.Context, batch WorkBatch, cause error) error {
	now := time.Now()
	for _, day := range batch.Days {
		var failures int
		_ = s.db.QueryRowContext(ctx, `SELECT failure_count FROM official_work WHERE product_key=? AND day=?`, batch.Product, day).Scan(&failures)
		failures++
		delay := officialRetryDelay(failures)
		_, err := s.writer.ExecContext(ctx, `
			UPDATE official_work SET state='pending',failure_count=?,next_attempt_ns=?,last_error=?,updated_at=?
			WHERE product_key=? AND day=?`,
			failures, now.Add(delay).UnixNano(), cause.Error(), now.UTC().Format(time.RFC3339Nano), batch.Product, day)
		if err != nil {
			return err
		}
	}
	return nil
}

func officialRetryDelay(failures int) time.Duration {
	base := time.Minute << min(max(failures-1, 0), 6)
	base = min(base, time.Hour)
	return min(time.Hour, base+time.Duration(rand.Int64N(int64(base/2)+1)))
}

func (s *Store) SaveOfficialBatch(ctx context.Context, batch WorkBatch, granules []model.Granule) error {
	if len(batch.Days) == 0 {
		return nil
	}
	byDay := make(map[string][]model.Granule, len(batch.Days))
	for _, granule := range granules {
		day := granule.Start.UTC().Format(time.DateOnly)
		byDay[day] = append(byDay[day], granule)
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, day := range batch.Days {
		if _, err := tx.ExecContext(ctx, `DELETE FROM official_assets WHERE product_key=? AND substr(slot_ts,1,10)=?`, batch.Product, day); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM official_slots WHERE product_key=? AND substr(slot_ts,1,10)=?`, batch.Product, day); err != nil {
			return err
		}
		items := byDay[day]
		assetCount := 0
		for _, granule := range items {
			slot := granule.Start.UTC().Format(time.RFC3339Nano)
			var totalSize int64
			representative := ""
			for _, asset := range granule.Assets {
				assetCount++
				totalSize += asset.Size
				if representative == "" {
					representative = asset.Name
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT OR REPLACE INTO official_assets(product_key,slot_ts,granule_id,filename,url,size,etag,media_type)
					VALUES(?,?,?,?,?,?,?,?)`,
					batch.Product, slot, granule.ID, asset.Name, asset.URL, asset.Size, asset.ETag, asset.MediaType); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO official_slots(product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at)
				VALUES(?,?,?,?,?,?,?)`,
				batch.Product, slot, granule.ID, len(granule.Assets), totalSize, representative, now); err != nil {
				return err
			}
		}
		state := "official_missing"
		if len(items) > 0 {
			state = "present"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO official_days(product_key,day,state,granule_count,asset_count,refreshed_at)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(product_key,day) DO UPDATE SET state=excluded.state,granule_count=excluded.granule_count,
			  asset_count=excluded.asset_count,refreshed_at=excluded.refreshed_at`,
			batch.Product, day, state, len(items), assetCount, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM official_work WHERE product_key=? AND day=?`, batch.Product, day); err != nil {
			return err
		}
		if err := s.reconcileLocalSlotsTx(ctx, tx, batch.Product, day, now); err != nil {
			return err
		}
		if err := s.recomputeDayTx(ctx, tx, batch.Product, day, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Year(ctx context.Context, product string, year int) (YearResponse, error) {
	if year < 1970 || year > 2100 {
		return YearResponse{}, errors.New("invalid year")
	}
	start, end := fmt.Sprintf("%04d-01-01", year), fmt.Sprintf("%04d-12-31", year)
	rows, err := s.db.QueryContext(ctx, `
		WITH days AS (
		  SELECT day FROM day_summary WHERE product_key=? AND day BETWEEN ? AND ?
		  UNION
		  SELECT day FROM official_work WHERE product_key=? AND day BETWEEN ? AND ?
		)
		SELECT days.day,COALESCE(s.status,'unknown'),COALESCE(s.official_count,0),COALESCE(s.local_ok_count,0),
		       COALESCE(s.missing_count,0),COALESCE(s.error_count,0),COALESCE(s.processing_count,0),
		       COALESCE(s.hourly_official_count,0),COALESCE(s.hourly_local_ok_count,0),
		       COALESCE(s.official_gap_count,0),COALESCE(s.hourly_official_gap_count,0),
		       s.updated_at,d.asset_count,
		       CASE WHEN w.day IS NULL THEN 0 ELSE 1 END
		FROM days
		LEFT JOIN day_summary s ON s.product_key=? AND s.day=days.day
		LEFT JOIN official_days d ON d.product_key=? AND d.day=days.day
		LEFT JOIN official_work w ON w.product_key=? AND w.day=days.day
		ORDER BY days.day`,
		product, start, end, product, start, end, product, product, product)
	if err != nil {
		return YearResponse{}, err
	}
	defer rows.Close()
	var days []YearDay
	for rows.Next() {
		var item YearDay
		var updated sql.NullString
		var assetCount sql.NullInt64
		if err := rows.Scan(&item.Day, &item.Status, &item.OfficialCount, &item.LocalOKCount, &item.MissingCount,
			&item.ErrorCount, &item.ProcessingCount, &item.HourlyOfficialCount, &item.HourlyLocalOKCount,
			&item.OfficialGapCount, &item.HourlyOfficialGapCount, &updated, &assetCount, &item.Indexing); err != nil {
			return YearResponse{}, err
		}
		if updated.Valid {
			value := updated.String
			item.UpdatedAt = &value
		}
		if assetCount.Valid {
			value := int(assetCount.Int64)
			item.OfficialFileCount = &value
		}
		days = append(days, item)
	}
	return YearResponse{Product: product, Year: year, Days: days}, rows.Err()
}

func (s *Store) Day(ctx context.Context, product, day string) (DayResponse, error) {
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return DayResponse{}, errors.New("invalid day")
	}
	_, productInfo, err := s.ProductSource(ctx, product)
	if err != nil {
		return DayResponse{}, err
	}
	profile := ProfileFor(productInfo)
	var officialDay *OfficialDay
	var od OfficialDay
	err = s.db.QueryRowContext(ctx, `
		SELECT state,granule_count,asset_count,refreshed_at FROM official_days WHERE product_key=? AND day=?`,
		product, day).Scan(&od.State, &od.GranuleCount, &od.AssetCount, &od.RefreshedAt)
	if err == nil {
		officialDay = &od
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DayResponse{}, err
	}
	officialRows, err := s.db.QueryContext(ctx, `
		SELECT slot_ts,granule_id,asset_count,total_size,representative_filename
		FROM official_slots WHERE product_key=? AND substr(slot_ts,1,10)=? ORDER BY slot_ts,granule_id`, product, day)
	if err != nil {
		return DayResponse{}, err
	}
	officialBy := map[string][]OfficialSlot{}
	for officialRows.Next() {
		var item OfficialSlot
		if err := officialRows.Scan(&item.SlotTS, &item.GranuleID, &item.AssetCount, &item.TotalSize, &item.RepresentativeFilename); err != nil {
			officialRows.Close()
			return DayResponse{}, err
		}
		officialBy[item.SlotTS] = append(officialBy[item.SlotTS], item)
	}
	officialRows.Close()
	localRows, err := s.db.QueryContext(ctx, `
		SELECT slot_ts,filename,size,state,detail,path FROM local_slots
		WHERE product_key=? AND substr(slot_ts,1,10)=? ORDER BY slot_ts,filename`, product, day)
	if err != nil {
		return DayResponse{}, err
	}
	localBy := map[string][]LocalSlot{}
	for localRows.Next() {
		var item LocalSlot
		if err := localRows.Scan(&item.SlotTS, &item.Filename, &item.Size, &item.State, &item.Detail, &item.Path); err != nil {
			localRows.Close()
			return DayResponse{}, err
		}
		localBy[item.SlotTS] = append(localBy[item.SlotTS], item)
	}
	localRows.Close()
	keys := map[string]struct{}{}
	for key := range officialBy {
		keys[key] = struct{}{}
	}
	for key := range localBy {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	axis := slotaxis.Resolve(productInfo.SourceID, productInfo.ProductKey, productInfo.Cadence)
	observed := make([]time.Time, 0, len(officialBy))
	for key := range officialBy {
		if at, err := time.Parse(time.RFC3339Nano, key); err == nil {
			observed = append(observed, at)
		}
	}
	tolerance := axis.Tolerance(observed)
	mode := axis.ClassifyWithin(observed, tolerance)
	if mode == "grid" {
		for _, at := range observed {
			nominal, _ := axis.NominalWithin(at, tolerance)
			if nominal.Format(time.DateOnly) != day {
				mode = "events"
				break
			}
		}
	}
	slots := make([]DaySlot, 0, len(ordered))
	for _, key := range ordered {
		status := "unknown"
		official := officialBy[key]
		local := localBy[key]
		if len(official) > 0 {
			expectedAssets := 0
			for _, row := range official {
				expectedAssets += row.AssetCount
			}
			okNames := map[string]struct{}{}
			hasError, hasProcessing := false, false
			for _, row := range local {
				switch row.State {
				case "ok":
					okNames[row.Filename] = struct{}{}
				case "error":
					hasError = true
				case "processing":
					hasProcessing = true
				}
			}
			switch {
			case hasError:
				status = "error"
			case len(okNames) >= expectedAssets && expectedAssets > 0:
				status = "complete"
			case hasProcessing:
				status = "processing"
			case len(okNames) > 0:
				status = "partial"
			default:
				status = "missing"
			}
		}
		slot := DaySlot{Slot: key, Status: status, Official: official, Local: local}
		if mode == "grid" {
			if at, err := time.Parse(time.RFC3339Nano, key); err == nil {
				if nominal, ok := axis.NominalWithin(at, tolerance); ok {
					slot.NominalSlot = nominal.Format(time.RFC3339)
				}
			}
		}
		slots = append(slots, slot)
	}
	if len(slots) == 0 && officialDay != nil && officialDay.State == "official_missing" {
		slots = append(slots, DaySlot{Slot: day + "T00:00:00Z", Status: "official_missing", Official: []OfficialSlot{}, Local: []LocalSlot{}})
	}
	var indexing int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM official_work WHERE product_key=? AND day=?`, product, day).Scan(&indexing)
	return DayResponse{
		Product: product, Day: day, AreaType: productInfo.AreaType, MatchToleranceSeconds: int(tolerance / time.Second),
		CadenceMinutes: profile.CadenceMinutes, TimelineMode: mode,
		OfficialGapCount: summaryOfficialGap(ctx, s.db, product, day), OfficialDay: officialDay,
		SummaryBasis: profile.SummaryBasis, Indexing: indexing, Slots: slots,
	}, nil
}

func (s *Store) ReplaceLocalFiles(ctx context.Context, files []LocalFile, fingerprint string) (bool, error) {
	return s.ReplaceLocalFilesWithWarnings(ctx, files, fingerprint, nil)
}

func (s *Store) ReplaceLocalFilesWithWarnings(ctx context.Context, files []LocalFile, fingerprint string, skipped []string) (bool, error) {
	var prior string
	_ = s.db.QueryRowContext(ctx, `SELECT fingerprint FROM local_scan_state WHERE id=1`).Scan(&prior)
	if prior == fingerprint {
		return false, nil
	}
	conn, err := s.writer.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if err := stageLocalFiles(ctx, conn, files); err != nil {
		return false, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	type pair struct{ product, day string }
	rows, err := tx.QueryContext(ctx, `
		WITH changed AS (
			SELECT f.filename FROM local_files f LEFT JOIN scanned_local_files n ON n.path=f.path
			WHERE n.path IS NULL OR f.filename<>n.filename OR f.size<>n.size OR f.mtime_ns<>n.mtime_ns OR f.state<>n.state OR f.detail<>n.detail
			UNION
			SELECT n.filename FROM scanned_local_files n LEFT JOIN local_files f ON f.path=n.path
			WHERE f.path IS NULL OR f.filename<>n.filename OR f.size<>n.size OR f.mtime_ns<>n.mtime_ns OR f.state<>n.state OR f.detail<>n.detail
		)
		SELECT DISTINCT a.product_key,substr(a.slot_ts,1,10)
		FROM changed JOIN official_assets a ON a.filename=changed.filename`)
	if err != nil {
		return false, err
	}
	var pairs []pair
	for rows.Next() {
		var item pair
		if err := rows.Scan(&item.product, &item.day); err != nil {
			rows.Close()
			return false, err
		}
		pairs = append(pairs, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM local_files WHERE NOT EXISTS (
			SELECT 1 FROM scanned_local_files n WHERE n.path=local_files.path
			AND n.filename=local_files.filename AND n.size=local_files.size AND n.mtime_ns=local_files.mtime_ns
			AND n.state=local_files.state AND n.detail=local_files.detail
		)`); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO local_files(path,filename,size,mtime_ns,state,detail,updated_at)
		SELECT n.path,n.filename,n.size,n.mtime_ns,n.state,n.detail,?
		FROM scanned_local_files n WHERE NOT EXISTS (SELECT 1 FROM local_files f WHERE f.path=n.path)`, now); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO local_scan_state(id,fingerprint,scanned_at) VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET fingerprint=excluded.fingerprint,scanned_at=excluded.scanned_at`,
		fingerprint, now); err != nil {
		return false, err
	}
	encodedSkipped, err := json.Marshal(skipped)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO archive_scan_status(id,skipped_paths_json,scanned_at) VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET skipped_paths_json=excluded.skipped_paths_json,scanned_at=excluded.scanned_at`, string(encodedSkipped), now); err != nil {
		return false, err
	}
	for _, item := range pairs {
		if err := s.reconcileLocalSlotsTx(ctx, tx, item.product, item.day, now); err != nil {
			return false, err
		}
		if err := s.recomputeDayTx(ctx, tx, item.product, item.day, now); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func stageLocalFiles(ctx context.Context, conn *sql.Conn, files []LocalFile) error {
	// Only the connection-local TEMP database is written here. Do not reserve
	// the main writer lock while inserting tens of thousands of scan results.
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS scanned_local_files (
		path TEXT PRIMARY KEY, filename TEXT NOT NULL, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL,
		state TEXT NOT NULL, detail TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	if _, err := conn.ExecContext(ctx, "DELETE FROM scanned_local_files"); err != nil {
		return err
	}
	stmt, err := conn.PrepareContext(ctx, `INSERT INTO scanned_local_files(path,filename,size,mtime_ns,state,detail) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, file := range files {
		if file.State == "" {
			file.State = "ok"
		}
		if _, err := stmt.ExecContext(ctx, file.Path, file.Filename, file.Size, file.MtimeNS, file.State, file.Detail); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) VerifiedArchiveFiles(ctx context.Context) (map[string]VerifiedArchiveFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,size,mtime_ns,sha256 FROM verified_archive_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make(map[string]VerifiedArchiveFile)
	for rows.Next() {
		var path string
		var file VerifiedArchiveFile
		if err := rows.Scan(&path, &file.Size, &file.MtimeNS, &file.SHA256); err != nil {
			return nil, err
		}
		files[path] = file
	}
	return files, rows.Err()
}

func (s *Store) RecordVerifiedArchiveFile(ctx context.Context, path string, size, mtimeNS int64, sha256, sourceID, collection, item, asset string) error {
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO verified_archive_files(path,size,mtime_ns,sha256,source_id,official_collection,official_item,official_asset,verified_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET size=excluded.size,mtime_ns=excluded.mtime_ns,
		sha256=excluded.sha256,source_id=excluded.source_id,official_collection=excluded.official_collection,
		official_item=excluded.official_item,official_asset=excluded.official_asset,verified_at=excluded.verified_at`,
		path, size, mtimeNS, sha256, sourceID, collection, item, asset, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) KnownLocalFilesBelow(ctx context.Context, dir string) ([]LocalFile, error) {
	lower, upper := dir+string(filepath.Separator), dir+"0"
	rows, err := s.db.QueryContext(ctx, `SELECT path,filename,size,mtime_ns,state,detail FROM local_files WHERE path>=? AND path<?`, lower, upper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []LocalFile
	for rows.Next() {
		var file LocalFile
		if err := rows.Scan(&file.Path, &file.Filename, &file.Size, &file.MtimeNS, &file.State, &file.Detail); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Store) ArchiveScanStatus(ctx context.Context) (ArchiveScanStatus, error) {
	var result ArchiveScanStatus
	var skipped string
	err := s.db.QueryRowContext(ctx, `SELECT skipped_paths_json,scanned_at FROM archive_scan_status WHERE id=1`).Scan(&skipped, &result.ScannedAt)
	if errors.Is(err, sql.ErrNoRows) {
		result.SkippedPaths = []string{}
	} else if err != nil {
		return result, err
	} else if err := json.Unmarshal([]byte(skipped), &result.SkippedPaths); err != nil {
		return result, err
	}
	s.scanMu.RLock()
	runtime := s.scanRuntime
	s.scanMu.RUnlock()
	result.State, result.StartedAt, result.Observed, result.LastError = runtime.state, runtime.started, runtime.observed, runtime.lastError
	if runtime.completed != "" {
		result.ScannedAt = runtime.completed
	}
	if result.State == "" {
		result.State = "pending"
		if result.ScannedAt != "" {
			result.State = "ready"
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),
		coalesce(sum(CASE WHEN state='unverified' THEN 1 ELSE 0 END),0),
		coalesce(sum(CASE WHEN state='error' THEN 1 ELSE 0 END),0)
		FROM local_files`).Scan(&result.Files, &result.Unverified, &result.Errors); err != nil {
		return result, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM local_files f
		JOIN verified_archive_files v ON v.path=f.path AND v.size=f.size AND v.mtime_ns=f.mtime_ns
		WHERE f.state='ok'`).Scan(&result.Verified); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) reconcileLocalSlotsTx(ctx context.Context, tx *sql.Tx, product, day, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM local_slots WHERE product_key=? AND substr(slot_ts,1,10)=?`, product, day); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO local_slots(product_key,slot_ts,path,filename,size,mtime_ns,state,detail,updated_at)
		SELECT a.product_key,a.slot_ts,f.path,f.filename,f.size,f.mtime_ns,
		       CASE WHEN a.size>0 AND a.size<>f.size THEN 'error' ELSE f.state END,
		       CASE WHEN a.size>0 AND a.size<>f.size THEN 'local size differs from official asset' ELSE f.detail END,?
		FROM official_assets a JOIN local_files f ON f.filename=a.filename
		WHERE a.product_key=? AND substr(a.slot_ts,1,10)=?`, now, product, day)
	return err
}

func (s *Store) recomputeDayTx(ctx context.Context, tx *sql.Tx, product, day, now string) error {
	var officialState, sourceID, cadence, areaType, dataType, instrument string
	err := tx.QueryRowContext(ctx, `
		SELECT d.state,p.source_id,p.cadence,p.area_type,p.data_type,p.instrument
		FROM official_days d JOIN products p ON p.product_key=d.product_key
		WHERE d.product_key=? AND d.day=?`, product, day).Scan(&officialState, &sourceID, &cadence, &areaType, &dataType, &instrument)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	productInfo := model.Product{ProductKey: product, SourceID: sourceID, Cadence: cadence, AreaType: areaType, DataType: dataType, Instrument: instrument}
	profile := ProfileFor(productInfo)
	axis := slotaxis.Resolve(sourceID, product, cadence)
	rows, err := tx.QueryContext(ctx, `
		WITH official AS (
		  SELECT slot_ts,count(DISTINCT filename) expected
		  FROM official_assets WHERE product_key=? AND substr(slot_ts,1,10)=?
		  GROUP BY slot_ts
		), local AS (
		  SELECT slot_ts,
		         count(DISTINCT CASE WHEN state='ok' THEN filename END) local_ok,
		         sum(CASE WHEN state='error' THEN 1 ELSE 0 END) errors,
		         sum(CASE WHEN state='processing' THEN 1 ELSE 0 END) processing
		  FROM local_slots WHERE product_key=? AND substr(slot_ts,1,10)=?
		  GROUP BY slot_ts
		)
		SELECT o.slot_ts,o.expected,coalesce(l.local_ok,0),coalesce(l.errors,0),coalesce(l.processing,0)
		FROM official o LEFT JOIN local l USING(slot_ts) ORDER BY o.slot_ts`,
		product, day, product, day)
	if err != nil {
		return err
	}
	type slot struct {
		ts                                  string
		expected, local, errors, processing int
	}
	var slots []slot
	for rows.Next() {
		var item slot
		if err := rows.Scan(&item.ts, &item.expected, &item.local, &item.errors, &item.processing); err != nil {
			rows.Close()
			return err
		}
		slots = append(slots, item)
	}
	rows.Close()
	observed := make([]time.Time, 0, len(slots))
	for _, item := range slots {
		if at, err := time.Parse(time.RFC3339Nano, item.ts); err == nil {
			observed = append(observed, at)
		}
	}
	tolerance := axis.Tolerance(observed)
	mode := axis.ClassifyWithin(observed, tolerance)
	officialCount := len(slots)
	localOK, localPresent, missing, errorCount, processingCount := 0, 0, 0, 0, 0
	hourly := make(map[time.Time]bool)
	nominalSlots := make(map[time.Time]struct{})
	for _, item := range slots {
		if item.local > 0 {
			localPresent++
		}
		complete := item.expected > 0 && item.local >= item.expected
		if item.errors > 0 && !complete {
			errorCount++
		} else if item.processing > 0 && !complete {
			processingCount++
		} else if complete {
			localOK++
		} else {
			missing++
		}
		if mode == "grid" {
			at, err := time.Parse(time.RFC3339Nano, item.ts)
			if err != nil {
				continue
			}
			nominal, ok := axis.NominalWithin(at, tolerance)
			if !ok || nominal.Format(time.DateOnly) != day {
				continue
			}
			nominalSlots[nominal] = struct{}{}
			if profile.HourlyBaseline && nominal.Minute() == 0 {
				previous, seen := hourly[nominal]
				hourly[nominal] = complete && (!seen || previous)
			}
		}
	}
	hourlyOfficial, hourlyOK := len(hourly), 0
	for _, complete := range hourly {
		if complete {
			hourlyOK++
		}
	}
	officialGap := 0
	if mode == "grid" && profile.ExpectedPerDay > 0 && len(nominalSlots) < profile.ExpectedPerDay {
		officialGap = profile.ExpectedPerDay - len(nominalSlots)
	}
	hourlyGap := 0
	if profile.HourlyBaseline && hourlyOfficial < 24 {
		hourlyGap = 24 - hourlyOfficial
	}
	status := "unknown"
	switch {
	case errorCount > 0:
		status = "error"
	case processingCount > 0:
		status = "processing"
	case officialState == "official_missing" && officialCount == 0:
		status = "official_missing"
	case officialCount > 0 && localOK == officialCount:
		if officialGap > 0 {
			status = "complete_official_gap"
		} else {
			status = "complete"
		}
	case profile.HourlyBaseline && hourlyOfficial > 0 && hourlyOK == hourlyOfficial:
		if hourlyGap > 0 {
			status = "hourly_complete_official_gap"
		} else {
			status = "hourly_complete"
		}
	case localPresent > 0:
		status = "partial"
	case missing > 0:
		status = "missing"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO day_summary(product_key,day,status,official_count,local_ok_count,missing_count,error_count,processing_count,
		 hourly_official_count,hourly_local_ok_count,official_gap_count,hourly_official_gap_count,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(product_key,day) DO UPDATE SET status=excluded.status,official_count=excluded.official_count,
		 local_ok_count=excluded.local_ok_count,missing_count=excluded.missing_count,error_count=excluded.error_count,
		 processing_count=excluded.processing_count,hourly_official_count=excluded.hourly_official_count,
		 hourly_local_ok_count=excluded.hourly_local_ok_count,official_gap_count=excluded.official_gap_count,
		 hourly_official_gap_count=excluded.hourly_official_gap_count,updated_at=excluded.updated_at`,
		product, day, status, officialCount, localOK, missing, errorCount, processingCount,
		hourlyOfficial, hourlyOK, officialGap, hourlyGap, now)
	return err
}

type Profile struct {
	CadenceMinutes int
	ExpectedPerDay int
	HourlyBaseline bool
	SummaryBasis   string
}

func ProfileFor(product model.Product) Profile {
	axis := slotaxis.Resolve(product.SourceID, product.ProductKey, product.Cadence)
	profile := Profile{SummaryBasis: "all_official_slots", CadenceMinutes: axis.CadenceMinutes}
	if profile.CadenceMinutes > 0 && 1440%profile.CadenceMinutes == 0 {
		profile.ExpectedPerDay = 1440 / profile.CadenceMinutes
	}
	area := strings.ToUpper(strings.TrimSpace(product.AreaType))
	name := strings.ToUpper(product.DataType)
	if area == "DISK" || strings.Contains(name, "RADF") || strings.Contains(name, "CMIPF") ||
		strings.Contains(name, "FLDK") || (strings.Contains(strings.ToUpper(product.Instrument), "FCI") && area == "DISK") {
		profile.HourlyBaseline = true
		profile.SummaryBasis = "all_with_hourly_disk_fallback"
	}
	return profile
}

func summaryOfficialGap(ctx context.Context, db *sql.DB, product, day string) int {
	var gap int
	_ = db.QueryRowContext(ctx, `SELECT official_gap_count FROM day_summary WHERE product_key=? AND day=?`, product, day).Scan(&gap)
	return gap
}

func dayFloor(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}
