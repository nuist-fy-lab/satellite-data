package downloadmon

import (
	"context"
	"log/slog"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type providerClient interface {
	Providers(context.Context) ([]downloader.ProviderStatus, error)
}

type Monitor struct {
	store    *state.Store
	client   providerClient
	interval time.Duration
}

func New(store *state.Store, client *downloader.Client) *Monitor {
	return &Monitor{store: store, client: client, interval: 15 * time.Second}
}

func (m *Monitor) Run(ctx context.Context) {
	if m == nil || m.store == nil || m.client == nil {
		return
	}
	go func() {
		m.sample(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sample(ctx)
			}
		}
	}()
	go func() {
		m.prune(ctx)
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.prune(ctx)
			}
		}
	}()
}

func (m *Monitor) sample(ctx context.Context) {
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	providers, err := m.client.Providers(requestCtx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Debug("read download monitor providers", "error", err)
		}
		return
	}
	if err := m.store.RecordDownloadSamples(ctx, time.Now().UTC(), providers); err != nil && ctx.Err() == nil {
		slog.Warn("record download monitor samples", "error", err)
	}
}

func (m *Monitor) prune(ctx context.Context) {
	if err := m.store.DeleteDownloadSamplesBefore(ctx, time.Now().UTC().Add(-8*24*time.Hour)); err != nil && ctx.Err() == nil {
		slog.Warn("prune download monitor samples", "error", err)
	}
}
