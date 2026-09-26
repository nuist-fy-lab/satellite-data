package state

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
)

type DownloadMonitorPoint struct {
	SampledAt           time.Time `json:"sampled_at"`
	BytesPerSecond      float64   `json:"bytes_per_second"`
	ActiveTransfers     int       `json:"active_transfers"`
	AdaptiveConcurrency int       `json:"adaptive_concurrency"`
	Queued              int       `json:"queued"`
	Verifying           int       `json:"verifying"`
}

type DownloadMonitorLatest struct {
	SourceID string `json:"source_id"`
	DownloadMonitorPoint
}

func (s *Store) RecordDownloadSamples(ctx context.Context, at time.Time, providers []downloader.ProviderStatus) error {
	if len(providers) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, provider := range providers {
		if provider.SourceID == "" {
			continue
		}
		rate := provider.BytesPerSecond
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			rate = 0
		}
		active, limit := max(0, provider.ActiveTransfers), max(0, provider.AdaptiveConcurrency)
		if !provider.Enabled {
			rate, active, limit = 0, 0, 0
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO download_monitor_samples(provider_id,sampled_at_ns,bytes_per_second,active_transfers,adaptive_concurrency,queued,verifying)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(provider_id,sampled_at_ns) DO UPDATE SET
			 bytes_per_second=excluded.bytes_per_second,active_transfers=excluded.active_transfers,
			 adaptive_concurrency=excluded.adaptive_concurrency,queued=excluded.queued,verifying=excluded.verifying`,
			provider.SourceID, at.UTC().UnixNano(), rate, active,
			limit, max(0, provider.States["queued"]+provider.States["retry_wait"]),
			max(0, provider.States["verifying"]))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteDownloadSamplesBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM download_monitor_samples WHERE sampled_at_ns<?`, cutoff.UTC().UnixNano())
	return err
}

func (s *Store) LatestDownloadSamples(ctx context.Context) ([]DownloadMonitorLatest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.provider_id,m.sampled_at_ns,m.bytes_per_second,m.active_transfers,m.adaptive_concurrency,m.queued,m.verifying
		FROM download_monitor_samples m
		JOIN (SELECT provider_id,max(sampled_at_ns) sampled_at_ns FROM download_monitor_samples GROUP BY provider_id) latest
		ON m.provider_id=latest.provider_id AND m.sampled_at_ns=latest.sampled_at_ns
		ORDER BY m.provider_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DownloadMonitorLatest{}
	for rows.Next() {
		var item DownloadMonitorLatest
		var ns int64
		if err := rows.Scan(&item.SourceID, &ns, &item.BytesPerSecond, &item.ActiveTransfers, &item.AdaptiveConcurrency, &item.Queued, &item.Verifying); err != nil {
			return nil, err
		}
		item.SampledAt = time.Unix(0, ns).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DownloadSeries(ctx context.Context, since time.Time, bucket time.Duration) (map[string][]DownloadMonitorPoint, error) {
	if bucket <= 0 {
		return nil, errors.New("download monitor bucket must be positive")
	}
	width := bucket.Nanoseconds()
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_id,(sampled_at_ns / ?) * ? bucket_ns,
		       avg(bytes_per_second),max(active_transfers),max(adaptive_concurrency),max(queued),max(verifying)
		FROM download_monitor_samples WHERE sampled_at_ns>=?
		GROUP BY provider_id,bucket_ns ORDER BY provider_id,bucket_ns`,
		width, width, since.UTC().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make(map[string][]DownloadMonitorPoint)
	for rows.Next() {
		var point DownloadMonitorPoint
		var source string
		var ns int64
		if err := rows.Scan(&source, &ns, &point.BytesPerSecond, &point.ActiveTransfers, &point.AdaptiveConcurrency, &point.Queued, &point.Verifying); err != nil {
			return nil, err
		}
		point.SampledAt = time.Unix(0, ns).UTC()
		items[source] = append(items[source], point)
	}
	return items, rows.Err()
}
