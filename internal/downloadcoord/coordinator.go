package downloadcoord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type Coordinator struct {
	registry   *provider.Registry
	store      *state.Store
	downloader *downloader.Client
	wg         sync.WaitGroup
}

func New(registry *provider.Registry, store *state.Store, client *downloader.Client) (*Coordinator, error) {
	if registry == nil || store == nil || client == nil {
		return nil, errors.New("download coordinator requires registry, store and downloader client")
	}
	return &Coordinator{registry: registry, store: store, downloader: client}, nil
}

func (c *Coordinator) Run(ctx context.Context) {
	for _, providerID := range []string{"goes", "himawari8", "himawari9", "modis", "viirs", "mtg", "earthcare", "calipso", "cloudsat"} {
		providerID := providerID
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.discoveryLoop(ctx, providerID)
		}()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.monitorLoop(ctx)
	}()
}

func ProviderForSource(sourceID string) (string, bool) {
	switch sourceID {
	case "goes16", "goes17", "goes18", "goes19":
		return "goes", true
	case "modis", "viirs", "mtg", "earthcare", "calipso", "cloudsat":
		return sourceID, true
	case "himawari8", "himawari9":
		return sourceID, true
	default:
		return "", false
	}
}

func (c *Coordinator) discoveryLoop(ctx context.Context, providerID string) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		request, ok, err := c.store.ClaimDownloadRequest(ctx, providerID)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Warn("claim download discovery", "provider", providerID, "error", err)
			}
			timer.Reset(500 * time.Millisecond)
			continue
		}
		if !ok {
			timer.Reset(200 * time.Millisecond)
			continue
		}
		if err := c.advanceDiscovery(ctx, request); err != nil {
			delay := discoveryRetryDelay(request.FailureCount + 1)
			_ = c.store.RetryDownloadDiscovery(context.WithoutCancel(ctx), request.ID, err, delay)
			slog.Warn("advance download discovery", "provider", providerID, "request", request.ID, "error", err)
			timer.Reset(25 * time.Millisecond)
			continue
		}
		timer.Reset(10 * time.Millisecond)
	}
}

func (c *Coordinator) advanceDiscovery(ctx context.Context, request state.DownloadRequest) error {
	p, err := c.registry.Provider(request.SourceID)
	if err != nil {
		return err
	}
	_, product, err := c.store.ProductSource(ctx, request.CollectionID)
	if err != nil {
		return fmt.Errorf("resolve product metadata: %w", err)
	}
	chunkEnd := request.Cursor.Add(discoveryChunk(request.ProviderID))
	if chunkEnd.After(request.End) {
		chunkEnd = request.End
	}
	if !chunkEnd.After(request.Cursor) {
		return c.store.AdvanceDownloadRequest(ctx, request.ID, request.End, 0, nil, true)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	granules, err := p.Search(queryCtx, model.Query{
		CollectionID: request.CollectionID,
		Start:        request.Cursor,
		End:          chunkEnd,
	})
	cancel()
	if err != nil {
		return err
	}
	refs := make([]state.DownloaderTaskRef, 0, len(granules))
	for _, granule := range granules {
		for _, asset := range granule.Assets {
			spec := downloader.TaskSpec{
				SourceID:     request.ProviderID,
				CollectionID: request.CollectionID,
				GranuleID:    granule.ID,
				AssetID:      asset.ID,
				Name:         asset.Name,
				URL:          asset.URL,
				AuthRef:      asset.AuthRef,
				RelativeDir:  archiveRelativeDir(request.SourceID, product, granule.Start),
				Size:         asset.Size,
				ETag:         asset.ETag,
				MediaType:    asset.MediaType,
			}
			task, _, err := c.downloader.Enqueue(ctx, spec)
			if err != nil {
				return err
			}
			refs = append(refs, state.DownloaderTaskRef{ID: task.ID, Key: task.Key, Filename: task.Name})
		}
	}
	return c.store.AdvanceDownloadRequest(ctx, request.ID, chunkEnd, len(granules), refs, !chunkEnd.Before(request.End))
}

func (c *Coordinator) monitorLoop(ctx context.Context) {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		requests, err := c.store.ActiveDownloadRequests(ctx, 100)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Warn("list active download requests", "error", err)
			}
			timer.Reset(time.Second)
			continue
		}
		for _, request := range requests {
			if err := c.refreshRequest(ctx, request); err != nil && !errors.Is(err, context.Canceled) {
				slog.Debug("refresh downloader request status", "request", request.ID, "error", err)
			}
		}
		timer.Reset(500 * time.Millisecond)
	}
}

func (c *Coordinator) refreshRequest(ctx context.Context, request state.DownloadRequest) error {
	ids, err := c.store.DownloadRequestTaskIDs(ctx, request.ID)
	if err != nil {
		return err
	}
	tasks := make([]downloader.Task, 0, len(ids))
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		items, err := c.downloader.TaskStatuses(ctx, ids[start:end])
		if err != nil {
			return err
		}
		tasks = append(tasks, items...)
	}
	return c.store.UpdateDownloadTransferState(ctx, request.ID, len(ids), tasks)
}

func discoveryChunk(providerID string) time.Duration {
	switch providerID {
	case "goes", "himawari8", "himawari9":
		return time.Hour
	case "mtg":
		return 3 * time.Hour
	case "cloudsat":
		return 24 * time.Hour
	default:
		return 6 * time.Hour
	}
}

func discoveryRetryDelay(failure int) time.Duration {
	if failure < 1 {
		failure = 1
	}
	delay := 5 * time.Second
	for i := 1; i < failure && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

var unsafePathChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func archiveRelativeDir(sourceID string, product model.Product, at time.Time) string {
	components := []string{
		safeComponent(sourceID),
		safeComponent(product.Satellite),
		safeComponent(product.Instrument),
		safeComponent(product.Level),
		safeComponent(product.DataType),
		at.UTC().Format("2006"),
		at.UTC().Format("01"),
		at.UTC().Format("02"),
	}
	out := make([]string, 0, len(components))
	for _, component := range components {
		if component != "" {
			out = append(out, component)
		}
	}
	return path.Join(out...)
}

func safeComponent(value string) string {
	value = strings.Trim(strings.TrimSpace(value), ".")
	value = unsafePathChars.ReplaceAllString(value, "_")
	return strings.Trim(value, "_")
}
