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
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
	"github.com/nuist-fy-lab/satellite-data/internal/slotaxis"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type Coordinator struct {
	registry   *provider.Registry
	store      *state.Store
	downloader *downloader.Client
	cds        CDSOrders
	wg         sync.WaitGroup
}

func New(registry *provider.Registry, store *state.Store, client *downloader.Client, cds ...CDSOrders) (*Coordinator, error) {
	if registry == nil || store == nil || client == nil {
		return nil, errors.New("download coordinator requires registry, store and downloader client")
	}
	c := &Coordinator{registry: registry, store: store, downloader: client}
	if len(cds) > 0 {
		c.cds = cds[0]
	}
	return c, nil
}

func (c *Coordinator) Run(ctx context.Context) {
	for _, providerID := range []string{"goes", "himawari8", "himawari9", "modis", "viirs", "mtg", "earthcare", "calipso", "cloudsat", "era5"} {
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
	case "modis", "viirs", "mtg", "earthcare", "calipso", "cloudsat", "era5":
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
			var response *era5.HTTPError
			invalidInputs := errors.As(err, &response) && (response.Status == 400 || response.Status == 422)
			if errors.Is(err, era5.ErrSubmissionUncertain) || errors.Is(err, era5.ErrInvalidRequest) || invalidInputs {
				_ = c.store.FailDownloadOrder(context.WithoutCancel(ctx), request.ID, err)
				timer.Reset(25 * time.Millisecond)
				continue
			}
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
	if request.ProviderID == "era5" {
		return c.advanceERA5(ctx, request)
	}
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
	axis := slotaxis.Resolve(request.SourceID, request.CollectionID, product.Cadence)
	searchStart, searchEnd := request.Cursor, chunkEnd
	if len(request.Selection.Times) > 0 {
		searchStart = searchStart.Add(-axis.Max)
		searchEnd = searchEnd.Add(axis.Max)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	granules, err := p.Search(queryCtx, model.Query{
		CollectionID: request.CollectionID,
		Start:        searchStart,
		End:          searchEnd,
	})
	cancel()
	if err != nil {
		return err
	}
	// Estimate the scan phase from the selected band, not other bands that
	// can start seconds apart within the same nominal observation.
	observed := make([]time.Time, 0, len(granules))
	for _, granule := range granules {
		for _, asset := range granule.Assets {
			if request.Selection.MatchesAsset(request.SourceID, request.CollectionID, asset.Name) {
				observed = append(observed, granule.Start)
				break
			}
		}
	}
	refs := make([]state.DownloaderTaskRef, 0, len(granules))
	selected := 0
	tolerance := axis.Tolerance(observed)
	for _, granule := range granules {
		nominal, match := axis.MatchWithin(granule.Start, request.Selection.Times, observed, tolerance)
		if !match || nominal.Before(request.Cursor) || !nominal.Before(chunkEnd) {
			continue
		}
		matched := false
		for _, asset := range granule.Assets {
			if !request.Selection.MatchesAsset(request.SourceID, request.CollectionID, asset.Name) {
				continue
			}
			matched = true
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
		if matched {
			selected++
		}
	}
	return c.store.AdvanceDownloadRequest(ctx, request.ID, chunkEnd, selected, refs, !chunkEnd.Before(request.End))
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
	productType := safeComponent(product.DataType)
	if productType == "" {
		productType = safeComponent(product.ProductKey)
	}
	if sourceID == "earthcare" {
		return path.Join(productType, at.UTC().Format("20060102"))
	}
	if sourceID == "era5" {
		return path.Join("CDS", productType, at.UTC().Format("2006/01/02"))
	}
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
